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

// getCurrency は Prebid.js (adgenerationBidAdapter.js: getCurrencyType) と同じ
// 二択ロジック: request.Cur に USD が含まれていれば "USD"、それ以外は "JPY"。
// 先頭通貨へのフォールバックは廃止 (EUR/GBP 等の素通しは仕様外)。
func (adg *AdgenerationAdapter) getCurrency(request *openrtb2.BidRequest) string {
	for _, c := range request.Cur {
		if strings.EqualFold(c, "USD") {
			return "USD"
		}
	}
	return adg.defaultCurrency
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

	// Prebid.js は bidRequests.data.ortb.imp[0] を直接参照するので、こちらも
	// 送信済 body から imp[0].id を取り出して対応 imp を引く。バックエンドが
	// locationid を返さない / 値がずれていても silent no-bid にしないため。
	if externalRequest == nil || len(externalRequest.Body) == 0 {
		return nil, nil
	}
	var sentBody adgRequestBody
	if err := jsonutil.Unmarshal(externalRequest.Body, &sentBody); err != nil {
		return nil, []error{err}
	}
	if len(sentBody.Ortb.Imp) == 0 {
		return nil, nil
	}
	targetImpID := sentBody.Ortb.Imp[0].ID
	var matchedImp *openrtb2.Imp
	for i := range internalRequest.Imp {
		if internalRequest.Imp[i].ID == targetImpID {
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
	// Native: バックエンドが返す native オブジェクトが OpenRTB native
	// response ({"native": {...assets, link, imptrackers...}}) と互換である前提。
	// Prebid.js (isNative) と同様、assets が非空のときのみ native として扱う。
	if len(adResult.Native) > 0 && imp.Native != nil && hasNativeAssets(adResult.Native) {
		// AdM は OpenRTB native admarkup の JSON 文字列。
		// バックエンドが {"native": {...}} 形式 / {assets:...} 直下のどちらでも、
		// 最終的に {"native":{...}} 形式に揃え、beaconurl を imptrackers に追加する
		// (Prebid.js: createNativeAd で beaconurl を impressionTrackers に push する挙動と一致)。
		admBytes, err := wrapNativeAdm(adResult.Native, adResult.Beaconurl)
		if err != nil {
			return "", "", err
		}
		return openrtb_ext.BidTypeNative, string(admBytes), nil
	}

	// Banner / Video-in-Banner
	ad := adResult.Ad
	if adResult.Vastxml != "" {
		// Prebid.js は location_params.option.ad_type === "upper_billboard" のとき
		// ADGBrowserM タグで差し込む。それ以外は APV タグ。
		if isUpperBillboard(locationParams) {
			ad = wrapWithADGBrowserM(adResult.Vastxml, extractMarginTop(imp))
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

// hasNativeAssets は results[0].native の生 JSON に assets[] が 1 件以上あるかを返す。
// Prebid.js isNative() (adResult.native.assets.length > 0) と同じ判定。
// {"native":{...}} と {assets:...} 直下のどちらの形でも受け付ける。
func hasNativeAssets(raw json.RawMessage) bool {
	var top map[string]json.RawMessage
	if err := jsonutil.Unmarshal(raw, &top); err != nil {
		return false
	}
	var assets json.RawMessage
	if inner, ok := top["native"]; ok {
		var nat map[string]json.RawMessage
		if err := jsonutil.Unmarshal(inner, &nat); err != nil {
			return false
		}
		assets = nat["assets"]
	} else {
		assets = top["assets"]
	}
	if len(assets) == 0 {
		return false
	}
	var arr []json.RawMessage
	if err := jsonutil.Unmarshal(assets, &arr); err != nil {
		return false
	}
	return len(arr) > 0
}

// wrapNativeAdm は results[0].native の生 JSON を AdM 用にラップし、
// beaconUrl を native.imptrackers に追加する。バックエンドが既に
// {"native":{...}} 形式で返す場合と {"assets":...} 直下で返す場合の両方を吸収する。
func wrapNativeAdm(raw json.RawMessage, beaconUrl string) ([]byte, error) {
	var top map[string]json.RawMessage
	if err := jsonutil.Unmarshal(raw, &top); err != nil {
		return nil, err
	}
	var native map[string]json.RawMessage
	if inner, ok := top["native"]; ok {
		if err := jsonutil.Unmarshal(inner, &native); err != nil {
			return nil, err
		}
	} else {
		native = top
	}

	if beaconUrl != "" {
		var trackers []string
		if rawTrackers, ok := native["imptrackers"]; ok {
			if err := jsonutil.Unmarshal(rawTrackers, &trackers); err != nil {
				return nil, err
			}
		}
		duplicate := false
		for _, t := range trackers {
			if t == beaconUrl {
				duplicate = true
				break
			}
		}
		if !duplicate {
			trackers = append(trackers, beaconUrl)
			encoded, err := json.Marshal(trackers)
			if err != nil {
				return nil, err
			}
			native["imptrackers"] = encoded
		}
	}

	nativeBytes, err := json.Marshal(native)
	if err != nil {
		return nil, err
	}
	return []byte(`{"native":` + string(nativeBytes) + `}`), nil
}

func isUpperBillboard(p *adgLocationParams) bool {
	if p == nil || p.Option == nil {
		return false
	}
	return p.Option.AdType == "upper_billboard"
}

func wrapWithAPV(impID, vastxml string) string {
	rep := regexp.MustCompile(`\r?\n`)
	replaced := rep.ReplaceAllString(vastxml, "")
	return "<body><div id=\"apvad-" + impID + "\"></div>" +
		"<script type=\"text/javascript\" id=\"apv\" src=\"https://cdn.apvdr.com/js/VideoAd.min.js\"></script>" +
		"<script type=\"text/javascript\"> (function(){ new APV.VideoAd({s:\"" + impID + "\"}).load('" + replaced + "'); })(); </script>" +
		"</body>"
}

func wrapWithADGBrowserM(vastxml, marginTop string) string {
	// Prebid.js は bidder params.marginTop を ADGBrowserM.init({marginTop}) に渡す。
	// Prebid Server では imp.ext.bidder.marginTop に置く (ExtImpAdgeneration.MarginTop)。
	// 未指定時は Prebid.js と同じく '0'。
	if marginTop == "" {
		marginTop = "0"
	}
	rep := regexp.MustCompile(`\r?\n`)
	replaced := rep.ReplaceAllString(vastxml, "")
	return "<body>" +
		"<script type=\"text/javascript\" src=\"https://i.socdm.com/sdk/js/adg-browser-m.js\"></script>" +
		"<script type=\"text/javascript\">window.ADGBrowserM.init({vastXml: '" + replaced + "', marginTop: '" + marginTop + "'});</script>" +
		"</body>"
}

// extractMarginTop は imp.ext.bidder.marginTop を取り出す。取得失敗時は空文字。
func extractMarginTop(imp *openrtb2.Imp) string {
	if imp == nil || len(imp.Ext) == 0 {
		return ""
	}
	adgExt, err := unmarshalExtImpAdgeneration(imp)
	if err != nil {
		return ""
	}
	return adgExt.MarginTop
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
		// Prebid.js v1.6.6 (ADGENE_PREBID_VERSION) と揃え、ADG プロトコルバージョンとして共通管理する。
		"1.6.6",
		"JPY",
	}
	return bidder, nil
}
