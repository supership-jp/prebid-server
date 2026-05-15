package adgeneration

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/prebid/openrtb/v20/openrtb2"
	"github.com/prebid/prebid-server/v4/adapters"
	"github.com/prebid/prebid-server/v4/config"
	"github.com/prebid/prebid-server/v4/errortypes"
	"github.com/prebid/prebid-server/v4/openrtb_ext"
	"github.com/prebid/prebid-server/v4/util/jsonutil"
	"github.com/prebid/prebid-server/v4/version"
)

// Prebid.js v1.6.6 (modules/adgenerationBidAdapter.js) とリクエスト/レスポンス
// 仕様を揃えるため、エンドポイントは /adgen/prebid (POST + JSON Body)、
// URLクエリは id / posall / sdktype のみとし、それ以外の情報は ortb body に
// 載せて送信する。詳細な懸念点は
// projects/prebid-server/research/parity-concerns.md を参照。

type AdgenerationAdapter struct {
	endpoint        string
	version         string
	defaultCurrency string
}

// adgRequestBody は POST body の JSON 構造。Prebid.js の `data` オブジェクト
// (currency / pbver / sdkname / adapterver / ortb / imark) と一致させる。
type adgRequestBody struct {
	Currency   string             `json:"currency"`
	Pbver      string             `json:"pbver"`
	Sdkname    string             `json:"sdkname"`
	Adapterver string             `json:"adapterver"`
	Ortb       openrtb2.BidRequest `json:"ortb"`
	// imark は native でないとき (= banner) のみ 1 を送る。
	// Prebid.js 側コメント「native以外にvideo等の対応が入った場合は要修正」を踏襲。
	Imark int `json:"imark,omitempty"`
}

// adgServerResponse はバックエンド (d.socdm.com/adgen/prebid) の応答形式。
// Prebid.js は body.results[0] から取り出すため、results 優先で読む。
type adgServerResponse struct {
	Locationid     string                 `json:"locationid"`
	LocationParams *adgLocationParams     `json:"location_params,omitempty"`
	Results        []adgResult            `json:"results"`
}

type adgLocationParams struct {
	Option *adgLocationOption `json:"option,omitempty"`
}

type adgLocationOption struct {
	AdType string `json:"ad_type,omitempty"`
}

type adgResult struct {
	Ad         string          `json:"ad"`
	Beacon     string          `json:"beacon"`
	Beaconurl  string          `json:"beaconurl"`
	Cpm        float64         `json:"cpm"`
	Creativeid string          `json:"creativeid"`
	Dealid     string          `json:"dealid"`
	H          uint64          `json:"h"`
	W          uint64          `json:"w"`
	Ttl        uint64          `json:"ttl"`
	Vastxml    string          `json:"vastxml,omitempty"`
	LandingUrl string          `json:"landing_url"`
	Scheduleid string          `json:"scheduleid"`
	Adomain    []string        `json:"adomain,omitempty"`
	Native     json.RawMessage `json:"native,omitempty"`
}

func (adg *AdgenerationAdapter) MakeRequests(request *openrtb2.BidRequest, reqInfo *adapters.ExtraRequestInfo) ([]*adapters.RequestData, []error) {
	if len(request.Imp) == 0 {
		return nil, []error{&errortypes.BadInput{Message: "No impression in the bid request"}}
	}

	headers := http.Header{}
	headers.Add("Content-Type", "application/json;charset=utf-8")
	headers.Add("Accept", "application/json")
	if request.Device != nil {
		if len(request.Device.UA) > 0 {
			headers.Add("User-Agent", request.Device.UA)
		}
		if len(request.Device.IP) > 0 {
			headers.Add("X-Forwarded-For", request.Device.IP)
		}
	}

	bidRequestArray := make([]*adapters.RequestData, 0, len(request.Imp))
	var errs []error

	// Prebid.js は imp ごとに 1 リクエスト発行する。Prebid Server も同様にする。
	for index := range request.Imp {
		req, err := adg.buildRequest(request, index, headers)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		bidRequestArray = append(bidRequestArray, req)
	}

	return bidRequestArray, errs
}

func (adg *AdgenerationAdapter) buildRequest(request *openrtb2.BidRequest, index int, headers http.Header) (*adapters.RequestData, error) {
	imp := request.Imp[index]
	adgExt, err := unmarshalExtImpAdgeneration(&imp)
	if err != nil {
		return nil, &errortypes.BadInput{Message: err.Error()}
	}

	uri, err := adg.buildUri(adgExt.Id)
	if err != nil {
		return nil, &errortypes.BadInput{Message: err.Error()}
	}

	body, err := adg.buildBody(request, imp)
	if err != nil {
		return nil, err
	}

	return &adapters.RequestData{
		Method:  http.MethodPost,
		Uri:     uri,
		Body:    body,
		Headers: headers,
		ImpIDs:  []string{imp.ID},
	}, nil
}

func (adg *AdgenerationAdapter) buildUri(id string) (string, error) {
	uriObj, err := url.Parse(adg.endpoint)
	if err != nil {
		return "", err
	}
	v := url.Values{}
	v.Set("id", id)
	v.Set("posall", "SSPLOC")
	// 懸念: Prebid.js は常に sdktype=0 (web 想定) を送る。Prebid Server は app
	// 経由でも呼ばれるため、本来は OS で 0/1/2 を出し分けたい (旧 upstream 実装)。
	// パリティ優先で 0 固定にしている。app 配信の挙動はバックエンド側で
	// ortb.app の有無を見て判定する想定。詳細は parity-concerns.md §4。
	v.Set("sdktype", "0")
	uriObj.RawQuery = v.Encode()
	return uriObj.String(), nil
}

func (adg *AdgenerationAdapter) buildBody(request *openrtb2.BidRequest, imp openrtb2.Imp) ([]byte, error) {
	// ortb には単一 imp 構成の BidRequest を入れる (Prebid.js の挙動と同じ)。
	// 元 request の他フィールド (site/app/device/user/source/regs/ext 等) は
	// そのまま温存し、FPD/UserID/schain/SUA 等が自然にバックエンドへ届くようにする。
	ortbReq := *request
	ortbReq.Imp = []openrtb2.Imp{imp}

	pbver := version.Ver
	if pbver == "" {
		pbver = version.VerUnknown
	}

	body := adgRequestBody{
		Currency:   adg.getCurrency(request),
		Pbver:      pbver,
		Sdkname:    "prebidserver",
		Adapterver: adg.version,
		Ortb:       ortbReq,
	}
	// imark: native でない (= banner 想定) のとき 1。
	// 懸念: Prebid.js 由来のフラグ。バックエンドでの正確な意味は未確認 (parity-concerns.md §8)。
	if imp.Native == nil {
		body.Imark = 1
	}

	return json.Marshal(body)
}

func unmarshalExtImpAdgeneration(imp *openrtb2.Imp) (*openrtb_ext.ExtImpAdgeneration, error) {
	var bidderExt adapters.ExtImpBidder
	var adgExt openrtb_ext.ExtImpAdgeneration
	if err := jsonutil.Unmarshal(imp.Ext, &bidderExt); err != nil {
		return nil, err
	}
	if err := jsonutil.Unmarshal(bidderExt.Bidder, &adgExt); err != nil {
		return nil, err
	}
	if adgExt.Id == "" {
		return nil, errors.New("No Location ID in ExtImpAdgeneration.")
	}
	return &adgExt, nil
}

func (adg *AdgenerationAdapter) getCurrency(request *openrtb2.BidRequest) string {
	if len(request.Cur) <= 0 {
		return adg.defaultCurrency
	}
	for _, c := range request.Cur {
		if adg.defaultCurrency == c {
			return c
		}
	}
	return request.Cur[0]
}

func (adg *AdgenerationAdapter) MakeBids(internalRequest *openrtb2.BidRequest, externalRequest *adapters.RequestData, response *adapters.ResponseData) (*adapters.BidderResponse, []error) {
	if response.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if response.StatusCode == http.StatusBadRequest {
		return nil, []error{&errortypes.BadInput{
			Message: fmt.Sprintf("Unexpected status code: %d. Run with request.debug = 1 for more info", response.StatusCode),
		}}
	}
	if response.StatusCode != http.StatusOK {
		return nil, []error{&errortypes.BadServerResponse{
			Message: fmt.Sprintf("Unexpected status code: %d. Run with request.debug = 1 for more info", response.StatusCode),
		}}
	}

	var bidResp adgServerResponse
	if err := jsonutil.Unmarshal(response.Body, &bidResp); err != nil {
		return nil, []error{err}
	}
	if len(bidResp.Results) == 0 {
		return nil, nil
	}

	// Prebid.js と同じく results[0] のみを採用 (1 imp / 1 リクエストのため)。
	adResult := bidResp.Results[0]

	// 対応する imp を locationid で逆引き。
	var matchedImp *openrtb2.Imp
	for i := range internalRequest.Imp {
		adgExt, err := unmarshalExtImpAdgeneration(&internalRequest.Imp[i])
		if err != nil {
			return nil, []error{&errortypes.BadServerResponse{Message: err.Error()}}
		}
		if adgExt.Id == bidResp.Locationid {
			matchedImp = &internalRequest.Imp[i]
			break
		}
	}
	if matchedImp == nil {
		return nil, nil
	}

	bidType, adm, err := buildAdMarkup(&adResult, bidResp.LocationParams, matchedImp)
	if err != nil {
		return nil, []error{err}
	}

	bid := openrtb2.Bid{
		ID:    bidResp.Locationid,
		ImpID: matchedImp.ID,
		AdM:   adm,
		Price: adResult.Cpm,
		W:     int64(adResult.W),
		H:     int64(adResult.H),
		CrID:  adResult.Creativeid,
		DealID: adResult.Dealid,
	}
	if len(adResult.Adomain) > 0 {
		bid.ADomain = adResult.Adomain
	}

	bidResponse := adapters.NewBidderResponseWithBidsCapacity(1)
	bidResponse.Currency = adg.getCurrency(internalRequest)
	bidResponse.Bids = append(bidResponse.Bids, &adapters.TypedBid{
		Bid:     &bid,
		BidType: bidType,
	})
	return bidResponse, nil
}

// buildAdMarkup は results[0] から AdM を構築する。Native レスポンスがあれば
// それを優先し、無ければ banner (vastxml があれば動画タグ差し込み) として返す。
func buildAdMarkup(adResult *adgResult, locationParams *adgLocationParams, imp *openrtb2.Imp) (openrtb_ext.BidType, string, error) {
	// Native: 懸念 — バックエンドが返す native オブジェクトが OpenRTB native
	// response ({"native": {...assets, link, imptrackers...}}) と互換である前提。
	// Prebid.js 実装からは asset id (1=title, 2=image, 3=icon, 4=sponsoredBy,
	// 5=body, 6=cta, 502=privacyLink) は OpenRTB 互換と見られる。
	// beaconurl は impressionTrackers に追加が必要だが、現状はパススルーとして
	// バックエンドが OpenRTB に整合した形で返すことを期待する (parity-concerns.md §7)。
	if len(adResult.Native) > 0 && imp.Native != nil {
		// AdM は OpenRTB native admarkup の JSON 文字列。
		// バックエンドが {"native": {...}} 形式で返す場合と {assets:...} のみで
		// 返す場合があり得るため、両対応で薄くラップする。
		admBytes := wrapNativeAdm(adResult.Native)
		return openrtb_ext.BidTypeNative, string(admBytes), nil
	}

	// Banner / Video-in-Banner
	ad := adResult.Ad
	if adResult.Vastxml != "" {
		// Prebid.js は location_params.option.ad_type === "upper_billboard" のとき
		// ADGBrowserM タグで差し込む。それ以外は APV タグ。
		if isUpperBillboard(locationParams) {
			ad = wrapWithADGBrowserM(adResult.Vastxml)
		} else {
			ad = wrapWithAPV(imp.ID, adResult.Vastxml)
		}
	}
	ad = appendChildToBody(ad, adResult.Beacon)
	if unwrapped := removeWrapper(ad); unwrapped != "" {
		ad = unwrapped
	}
	return openrtb_ext.BidTypeBanner, ad, nil
}

// wrapNativeAdm は results[0].native の生 JSON を AdM 用にラップする。
// バックエンドが既に {"native":{...}} 形式で返している場合はそのまま、
// {"assets":...} 直下なら {"native":...} で包む。
func wrapNativeAdm(raw json.RawMessage) []byte {
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "{") {
		// すでに { から始まる場合の判定: native キーを含むか粗くチェック。
		// 厳密にやるなら一度 unmarshal するが、性能優先でプレフィックスのみ確認。
		if strings.Contains(trimmed[:min(64, len(trimmed))], "\"native\"") {
			return []byte(trimmed)
		}
	}
	return []byte(`{"native":` + trimmed + `}`)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func isUpperBillboard(p *adgLocationParams) bool {
	if p == nil || p.Option == nil {
		return false
	}
	return p.Option.AdType == "upper_billboard"
}

func wrapWithAPV(impID, vastxml string) string {
	rep := regexp.MustCompile(`/\r?\n/g`)
	replaced := rep.ReplaceAllString(vastxml, "")
	return "<body><div id=\"apvad-" + impID + "\"></div>" +
		"<script type=\"text/javascript\" id=\"apv\" src=\"https://cdn.apvdr.com/js/VideoAd.min.js\"></script>" +
		"<script type=\"text/javascript\"> (function(){ new APV.VideoAd({s:\"" + impID + "\"}).load('" + replaced + "'); })(); </script>" +
		"</body>"
}

func wrapWithADGBrowserM(vastxml string) string {
	// 懸念: Prebid.js は params.marginTop を使うが、Prebid Server には imp.ext.params
	// の概念がそのままは無いため、現状は marginTop=0 固定。必要なら ExtImpAdgeneration
	// に MarginTop を追加する (parity-concerns.md §10)。
	rep := regexp.MustCompile(`/\r?\n/g`)
	replaced := rep.ReplaceAllString(vastxml, "")
	return "<body>" +
		"<script type=\"text/javascript\" src=\"https://i.socdm.com/sdk/js/adg-browser-m.js\"></script>" +
		"<script type=\"text/javascript\">window.ADGBrowserM.init({vastXml: '" + replaced + "', marginTop: '0'});</script>" +
		"</body>"
}

func appendChildToBody(ad string, data string) string {
	rep := regexp.MustCompile(`<\/\s?body>`)
	return rep.ReplaceAllString(ad, data+"</body>")
}

func removeWrapper(ad string) string {
	bodyIndex := strings.Index(ad, "<body>")
	lastBodyIndex := strings.LastIndex(ad, "</body>")
	if bodyIndex == -1 || lastBodyIndex == -1 {
		return ""
	}
	str := strings.TrimSpace(strings.Replace(strings.Replace(ad[bodyIndex:lastBodyIndex], "<body>", "", 1), "</body>", "", 1))
	return str
}

// Builder builds a new instance of the Adgeneration adapter for the given bidder with the given config.
func Builder(bidderName openrtb_ext.BidderName, config config.Adapter, server config.Server) (adapters.Bidder, error) {
	bidder := &AdgenerationAdapter{
		config.Endpoint,
		"1.0.3",
		"JPY",
	}
	return bidder, nil
}
