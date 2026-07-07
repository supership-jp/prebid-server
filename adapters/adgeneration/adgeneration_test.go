package adgeneration

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/prebid/openrtb/v20/openrtb2"
	"github.com/prebid/prebid-server/v4/adapters"
	"github.com/prebid/prebid-server/v4/adapters/adapterstest"
	"github.com/prebid/prebid-server/v4/config"
	"github.com/prebid/prebid-server/v4/errortypes"
	"github.com/prebid/prebid-server/v4/openrtb_ext"
	"github.com/stretchr/testify/assert"
)

const testEndpoint = "https://d.socdm.com/adgen/prebid"

func newTestAdapter(t *testing.T) *AdgenerationAdapter {
	t.Helper()
	bidder, err := Builder(openrtb_ext.BidderAdgeneration, config.Adapter{Endpoint: testEndpoint},
		config.Server{ExternalUrl: "http://hosturl.com", GvlID: 1, DataCenter: "2"})
	if err != nil {
		t.Fatalf("Builder returned unexpected error: %v", err)
	}
	return bidder.(*AdgenerationAdapter)
}

func TestJsonSamples(t *testing.T) {
	bidder, err := Builder(openrtb_ext.BidderAdgeneration, config.Adapter{Endpoint: testEndpoint},
		config.Server{ExternalUrl: "http://hosturl.com", GvlID: 1, DataCenter: "2"})
	if err != nil {
		t.Fatalf("Builder returned unexpected error: %v", err)
	}
	adapterstest.RunJSONBidderTest(t, "adgenerationtest", bidder)
}

func TestBuildRequestPostsToAdgenPrebid(t *testing.T) {
	adg := newTestAdapter(t)
	req := &openrtb2.BidRequest{
		ID: "test",
		Imp: []openrtb2.Imp{
			{
				ID:     "imp-1",
				Banner: &openrtb2.Banner{Format: []openrtb2.Format{{W: 300, H: 250}}},
				Ext:    json.RawMessage(`{"bidder":{"id":"58278"}}`),
			},
		},
		Source: &openrtb2.Source{TID: "src-tid"},
		Device: &openrtb2.Device{UA: "testUA", IP: "1.2.3.4"},
		Site:   &openrtb2.Site{Page: "https://supership.com"},
		User:   &openrtb2.User{BuyerUID: "buyerID"},
	}

	requests, errs := adg.MakeRequests(req, &adapters.ExtraRequestInfo{})
	assert.Empty(t, errs)
	assert.Len(t, requests, 1)

	r := requests[0]
	assert.Equal(t, http.MethodPost, r.Method)
	assert.Equal(t, []string{"imp-1"}, r.ImpIDs)
	assert.Equal(t, "testUA", r.Headers.Get("User-Agent"))
	assert.Equal(t, "1.2.3.4", r.Headers.Get("X-Forwarded-For"))

	parsed, err := url.Parse(r.Uri)
	assert.NoError(t, err)
	assert.Equal(t, "d.socdm.com", parsed.Host)
	assert.Equal(t, "/adgen/prebid", parsed.Path)
	q := parsed.Query()
	assert.Equal(t, "58278", q.Get("id"))
	assert.Equal(t, "SSPLOC", q.Get("posall"))
	assert.Equal(t, "0", q.Get("sdktype"))
	// パリティ確認: 旧 upstream で送っていた以下のクエリを送らない。
	for _, key := range []string{"hb", "t", "currency", "sdkname", "adapterver", "sizes", "tp", "transactionid", "appbundle", "appname", "idfa", "advertising_id"} {
		assert.False(t, q.Has(key), "query %q should not be set", key)
	}

	var body adgRequestBody
	assert.NoError(t, json.Unmarshal(r.Body, &body))
	assert.Equal(t, "JPY", body.Currency)
	assert.Equal(t, "prebidserver", body.Sdkname)
	assert.Equal(t, "1.6.6", body.Adapterver)
	assert.Equal(t, 1, body.Imark, "banner request should set imark=1")
	assert.NotEmpty(t, body.Pbver)
	assert.Len(t, body.Ortb.Imp, 1)
	assert.Equal(t, "imp-1", body.Ortb.Imp[0].ID)
	assert.Equal(t, "https://supership.com", body.Ortb.Site.Page)
	assert.Equal(t, "src-tid", body.Ortb.Source.TID)
}

func TestBuildRequestForNativeOmitsImark(t *testing.T) {
	adg := newTestAdapter(t)
	req := &openrtb2.BidRequest{
		ID: "test",
		Imp: []openrtb2.Imp{
			{
				ID:     "imp-native",
				Native: &openrtb2.Native{Request: `{}`},
				Ext:    json.RawMessage(`{"bidder":{"id":"58278"}}`),
			},
		},
	}
	requests, errs := adg.MakeRequests(req, &adapters.ExtraRequestInfo{})
	assert.Empty(t, errs)
	assert.Len(t, requests, 1)

	var body adgRequestBody
	assert.NoError(t, json.Unmarshal(requests[0].Body, &body))
	assert.Equal(t, 0, body.Imark, "native request must not set imark")
}

func TestBuildRequestRejectsBadExt(t *testing.T) {
	adg := newTestAdapter(t)
	req := &openrtb2.BidRequest{
		ID: "test",
		Imp: []openrtb2.Imp{
			{ID: "imp-bad", Banner: &openrtb2.Banner{Format: []openrtb2.Format{{W: 300, H: 250}}}, Ext: json.RawMessage(`{"bidder":{"_id":"58278"}}`)},
			{ID: "imp-ok", Banner: &openrtb2.Banner{Format: []openrtb2.Format{{W: 300, H: 250}}}, Ext: json.RawMessage(`{"bidder":{"id":"58278"}}`)},
		},
	}
	requests, errs := adg.MakeRequests(req, &adapters.ExtraRequestInfo{})
	assert.Len(t, errs, 1)
	assert.Len(t, requests, 1, "valid imp should still produce a request")
}

// TestDetectSdkType は channel + device.os から sdktype を導出する挙動を網羅する。
// バックエンド `/adgen/prebid` が sdktype で配信ロジックを切り替える前提。
func TestDetectSdkType(t *testing.T) {
	cases := []struct {
		name string
		req  *openrtb2.BidRequest
		want string
	}{
		{
			name: "web: channel=pbjs / site only",
			req: &openrtb2.BidRequest{
				Ext:  json.RawMessage(`{"prebid":{"channel":{"name":"pbjs","version":"9"}}}`),
				Site: &openrtb2.Site{Page: "https://example.com/"},
			},
			want: "0",
		},
		{
			name: "web: channel=amp",
			req: &openrtb2.BidRequest{
				Ext:  json.RawMessage(`{"prebid":{"channel":{"name":"amp"}}}`),
				Site: &openrtb2.Site{Page: "https://example.com/"},
			},
			want: "0",
		},
		{
			name: "mobile app android via channel",
			req: &openrtb2.BidRequest{
				Ext:    json.RawMessage(`{"prebid":{"channel":{"name":"app"}}}`),
				App:    &openrtb2.App{Bundle: "com.example.app"},
				Device: &openrtb2.Device{OS: "android"},
			},
			want: "1",
		},
		{
			name: "mobile app ios via channel (case insensitive)",
			req: &openrtb2.BidRequest{
				Ext:    json.RawMessage(`{"prebid":{"channel":{"name":"APP"}}}`),
				App:    &openrtb2.App{Bundle: "com.example.app"},
				Device: &openrtb2.Device{OS: "iOS"},
			},
			want: "2",
		},
		{
			name: "fallback: channel 不在で BidRequest.App のみ (android)",
			req: &openrtb2.BidRequest{
				App:    &openrtb2.App{Bundle: "com.example.app"},
				Device: &openrtb2.Device{OS: "android"},
			},
			want: "1",
		},
		{
			name: "fallback: channel 不在で BidRequest.App のみ (ios)",
			req: &openrtb2.BidRequest{
				App:    &openrtb2.App{Bundle: "com.example.app"},
				Device: &openrtb2.Device{OS: "ios"},
			},
			want: "2",
		},
		{
			name: "app context だが device.os 不明 → 0",
			req: &openrtb2.BidRequest{
				Ext:    json.RawMessage(`{"prebid":{"channel":{"name":"app"}}}`),
				App:    &openrtb2.App{Bundle: "com.example.app"},
				Device: &openrtb2.Device{OS: "tvos"},
			},
			want: "0",
		},
		{
			name: "app と site が両方 nil → web 扱い",
			req:  &openrtb2.BidRequest{},
			want: "0",
		},
		{
			name: "app + site 両方ある場合は channel 優先 (channel=app)",
			req: &openrtb2.BidRequest{
				Ext:    json.RawMessage(`{"prebid":{"channel":{"name":"app"}}}`),
				App:    &openrtb2.App{Bundle: "com.example.app"},
				Site:   &openrtb2.Site{Page: "https://example.com/"},
				Device: &openrtb2.Device{OS: "android"},
			},
			want: "1",
		},
		{
			name: "壊れた ext は channel 不在として扱う (= site があれば web)",
			req: &openrtb2.BidRequest{
				Ext:  json.RawMessage(`{not-json`),
				Site: &openrtb2.Site{Page: "https://example.com/"},
			},
			want: "0",
		},
		{
			name: "ext.prebid はあるが channel なし (= site があれば web)",
			req: &openrtb2.BidRequest{
				Ext:  json.RawMessage(`{"prebid":{}}`),
				Site: &openrtb2.Site{Page: "https://example.com/"},
			},
			want: "0",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, detectSdkType(c.req))
		})
	}
}

// TestGetCurrency は Prebid.js (adgenerationBidAdapter.js: getCurrencyType) と同じ
// 二択挙動: USD を含めば USD、それ以外は JPY。
func TestGetCurrency(t *testing.T) {
	adg := newTestAdapter(t)
	cases := []struct {
		name string
		cur  []string
		want string
	}{
		{"default JPY when empty", nil, "JPY"},
		{"USD wins over JPY", []string{"USD", "JPY"}, "USD"},
		{"USD only", []string{"USD"}, "USD"},
		{"unrelated currency falls back to JPY", []string{"EUR"}, "JPY"},
		{"JPY only", []string{"JPY"}, "JPY"},
		{"case-insensitive usd", []string{"usd"}, "USD"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := adg.getCurrency(&openrtb2.BidRequest{Cur: c.cur})
			assert.Equal(t, c.want, got)
		})
	}
}

func TestBuildAdMarkupBanner(t *testing.T) {
	adResult := &adgResult{
		Ad:        "<!DOCTYPE html><body><div id=\"x\"></div></body>",
		Beacon:    "<img src=\"https://b.example/\">",
		Beaconurl: "https://b.example/",
		Cpm:       50,
	}
	imp := &openrtb2.Imp{ID: "imp-1", Banner: &openrtb2.Banner{}}

	bidType, adm, err := buildAdMarkup(adResult, nil, imp)
	assert.NoError(t, err)
	assert.Equal(t, openrtb_ext.BidTypeBanner, bidType)
	assert.Equal(t, "<div id=\"x\"></div><img src=\"https://b.example/\">", adm)
}

func TestBuildAdMarkupVastUsesAPV(t *testing.T) {
	adResult := &adgResult{
		Ad:      "<!DOCTYPE html><body></body>",
		Beacon:  "<img src=\"https://b.example/\">",
		Vastxml: "<VAST/>",
	}
	imp := &openrtb2.Imp{ID: "imp-vast", Banner: &openrtb2.Banner{}}
	bidType, adm, err := buildAdMarkup(adResult, nil, imp)
	assert.NoError(t, err)
	assert.Equal(t, openrtb_ext.BidTypeBanner, bidType)
	assert.Contains(t, adm, "apvad-imp-vast")
	assert.Contains(t, adm, "cdn.apvdr.com/js/VideoAd.min.js")
}

// vastxml に含まれる改行が JS 文字列リテラル内に残らないこと (Prebid.js: /\r?\n/g 相当)。
func TestBuildAdMarkupVastStripsNewlinesInsideJsLiteral(t *testing.T) {
	adResult := &adgResult{
		Ad:      "<!DOCTYPE html><body></body>",
		Vastxml: "<VAST>\r\nfoo\nbar\n</VAST>",
	}
	imp := &openrtb2.Imp{ID: "imp-vast", Banner: &openrtb2.Banner{}}
	_, adm, err := buildAdMarkup(adResult, nil, imp)
	assert.NoError(t, err)
	// APV.VideoAd(...).load('...') の引数内に改行が残ると JS 文字列が壊れる。
	assert.NotContains(t, adm, "load('<VAST>\r\nfoo")
	assert.NotContains(t, adm, "load('<VAST>\nfoo")
	// VAST は percent-encoding して decodeURIComponent で復元する
	// (adm に生の "<VAST" が残ると Prebid Mobile iOS が VAST と誤判定するため)。
	assert.Contains(t, adm, "load(decodeURIComponent('%3CVAST%3Efoobar%3C%2FVAST%3E'))")
	assert.NotContains(t, adm, "<VAST>")
}

func TestBuildAdMarkupADGBrowserMStripsNewlines(t *testing.T) {
	adResult := &adgResult{
		Ad:      "<!DOCTYPE html><body></body>",
		Vastxml: "<VAST>\r\nfoo\n</VAST>",
	}
	loc := &adgLocationParams{Option: &adgLocationOption{AdType: "upper_billboard"}}
	imp := &openrtb2.Imp{ID: "imp-ub", Banner: &openrtb2.Banner{}}
	_, adm, err := buildAdMarkup(adResult, loc, imp)
	assert.NoError(t, err)
	assert.NotContains(t, adm, "vastXml: '<VAST>\r\nfoo")
	// VAST は percent-encoding して decodeURIComponent で復元する
	// (adm に生の "<VAST" が残ると Prebid Mobile iOS が VAST と誤判定するため)。
	assert.Contains(t, adm, "vastXml: decodeURIComponent('%3CVAST%3Efoo%3C%2FVAST%3E')")
	assert.NotContains(t, adm, "<VAST>")
}

func TestBuildAdMarkupVastUsesADGBrowserMOnUpperBillboard(t *testing.T) {
	adResult := &adgResult{
		Ad:      "<!DOCTYPE html><body></body>",
		Beacon:  "<img src=\"https://b.example/\">",
		Vastxml: "<VAST/>",
	}
	loc := &adgLocationParams{Option: &adgLocationOption{AdType: "upper_billboard"}}
	imp := &openrtb2.Imp{ID: "imp-ub", Banner: &openrtb2.Banner{}}
	bidType, adm, err := buildAdMarkup(adResult, loc, imp)
	assert.NoError(t, err)
	assert.Equal(t, openrtb_ext.BidTypeBanner, bidType)
	assert.Contains(t, adm, "adg-browser-m.js")
	assert.NotContains(t, adm, "apvad-")
	// marginTop 未指定時は Prebid.js と同じく '0' を埋める。
	assert.Contains(t, adm, "marginTop: '0'")
}

func TestBuildAdMarkupVastADGBrowserMUsesBidderMarginTop(t *testing.T) {
	adResult := &adgResult{
		Ad:      "<!DOCTYPE html><body></body>",
		Vastxml: "<VAST/>",
	}
	loc := &adgLocationParams{Option: &adgLocationOption{AdType: "upper_billboard"}}
	imp := &openrtb2.Imp{
		ID:     "imp-ub",
		Banner: &openrtb2.Banner{},
		Ext:    json.RawMessage(`{"bidder":{"id":"58278","marginTop":"42"}}`),
	}
	_, adm, err := buildAdMarkup(adResult, loc, imp)
	assert.NoError(t, err)
	assert.Contains(t, adm, "marginTop: '42'")
}

func TestBuildAdMarkupNative(t *testing.T) {
	rawNative := json.RawMessage(`{"assets":[{"id":1,"title":{"text":"hello"}}],"link":{"url":"https://l.example/"}}`)
	adResult := &adgResult{Native: rawNative}
	imp := &openrtb2.Imp{ID: "imp-native", Native: &openrtb2.Native{Request: `{}`}}

	bidType, adm, err := buildAdMarkup(adResult, nil, imp)
	assert.NoError(t, err)
	assert.Equal(t, openrtb_ext.BidTypeNative, bidType)
	assert.True(t, strings.HasPrefix(adm, `{"native":`))
	assert.Contains(t, adm, `"assets"`)
}

func TestBuildAdMarkupNativeAppendsBeaconUrlToImptrackers(t *testing.T) {
	rawNative := json.RawMessage(`{"assets":[{"id":1,"title":{"text":"hello"}}],"link":{"url":"https://l.example/"},"imptrackers":["https://existing.example/imp"]}`)
	adResult := &adgResult{Native: rawNative, Beaconurl: "https://tg.example/bc"}
	imp := &openrtb2.Imp{ID: "imp-native", Native: &openrtb2.Native{Request: `{}`}}

	_, adm, err := buildAdMarkup(adResult, nil, imp)
	assert.NoError(t, err)
	assert.Contains(t, adm, "https://existing.example/imp")
	assert.Contains(t, adm, "https://tg.example/bc", "beaconurl must be appended to imptrackers")
}

func TestBuildAdMarkupNativeBeaconUrlDeduplicated(t *testing.T) {
	rawNative := json.RawMessage(`{"assets":[{"id":1,"title":{"text":"hi"}}],"imptrackers":["https://tg.example/bc"]}`)
	adResult := &adgResult{Native: rawNative, Beaconurl: "https://tg.example/bc"}
	imp := &openrtb2.Imp{ID: "imp-native", Native: &openrtb2.Native{Request: `{}`}}

	_, adm, err := buildAdMarkup(adResult, nil, imp)
	assert.NoError(t, err)
	// 既に imptrackers に含まれている場合は重複追加しない。
	assert.Equal(t, 1, strings.Count(adm, "https://tg.example/bc"))
}

// Prebid.js isNative() 互換: assets が空/欠落なら native ではなく banner として扱う。
func TestBuildAdMarkupFallsBackToBannerWhenNativeAssetsMissing(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"empty assets", `{"assets":[],"link":{"url":"https://l.example/"}}`},
		{"no assets key", `{"link":{"url":"https://l.example/"}}`},
		{"wrapped empty assets", `{"native":{"assets":[]}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			adResult := &adgResult{
				Native: json.RawMessage(c.raw),
				Ad:     "<body>fallback banner</body>",
			}
			imp := &openrtb2.Imp{ID: "imp-native", Native: &openrtb2.Native{Request: `{}`}}
			bidType, adm, err := buildAdMarkup(adResult, nil, imp)
			assert.NoError(t, err)
			assert.Equal(t, openrtb_ext.BidTypeBanner, bidType)
			assert.Equal(t, "fallback banner", adm)
		})
	}
}

func TestBuildAdMarkupNativeAcceptsWrappedInput(t *testing.T) {
	rawNative := json.RawMessage(`{"native":{"assets":[{"id":1,"title":{"text":"hi"}}]}}`)
	adResult := &adgResult{Native: rawNative, Beaconurl: "https://tg.example/bc"}
	imp := &openrtb2.Imp{ID: "imp-native", Native: &openrtb2.Native{Request: `{}`}}

	_, adm, err := buildAdMarkup(adResult, nil, imp)
	assert.NoError(t, err)
	assert.True(t, strings.HasPrefix(adm, `{"native":`))
	// 入れ子 native を二重ラップしない (= 出力に "native" は 1 回しか現れない)。
	assert.Equal(t, 1, strings.Count(adm, `"native"`))
	assert.Contains(t, adm, "https://tg.example/bc")
}

func TestMakeBidsReadsResultsAndAdomain(t *testing.T) {
	adg := newTestAdapter(t)

	internalRequest := &openrtb2.BidRequest{
		ID: "test",
		Imp: []openrtb2.Imp{
			{ID: "imp-1", Banner: &openrtb2.Banner{Format: []openrtb2.Format{{W: 300, H: 250}}}, Ext: json.RawMessage(`{"bidder":{"id":"58278"}}`)},
		},
	}
	respBody := `{
		"locationid": "58278",
		"results": [{
			"ad": "<body>testAd</body>",
			"beacon": "",
			"cpm": 30,
			"creativeid": "Dummy_supership.jp",
			"dealid": "test-deal",
			"h": 250,
			"w": 300,
			"adomain": ["advertiser.example"]
		}]
	}`
	resp := &adapters.ResponseData{StatusCode: 200, Body: []byte(respBody)}

	sentBody, _ := json.Marshal(adgRequestBody{Ortb: openrtb2.BidRequest{Imp: []openrtb2.Imp{{ID: "imp-1"}}}})
	bidderResp, errs := adg.MakeBids(internalRequest, &adapters.RequestData{Body: sentBody}, resp)
	assert.Empty(t, errs)
	assert.NotNil(t, bidderResp)
	assert.Equal(t, "JPY", bidderResp.Currency)
	assert.Len(t, bidderResp.Bids, 1)

	bid := bidderResp.Bids[0]
	assert.Equal(t, openrtb_ext.BidTypeBanner, bid.BidType)
	assert.Equal(t, "58278", bid.Bid.ID)
	assert.Equal(t, "imp-1", bid.Bid.ImpID)
	assert.Equal(t, "testAd", bid.Bid.AdM)
	assert.Equal(t, 30.0, bid.Bid.Price)
	assert.Equal(t, int64(300), bid.Bid.W)
	assert.Equal(t, int64(250), bid.Bid.H)
	assert.Equal(t, "Dummy_supership.jp", bid.Bid.CrID)
	assert.Equal(t, "test-deal", bid.Bid.DealID)
	assert.Equal(t, []string{"advertiser.example"}, bid.Bid.ADomain)
}

func TestMakeBidsReturnsNilOnNoContent(t *testing.T) {
	adg := newTestAdapter(t)
	resp := &adapters.ResponseData{StatusCode: http.StatusNoContent}
	bidderResp, errs := adg.MakeBids(&openrtb2.BidRequest{}, &adapters.RequestData{}, resp)
	assert.Nil(t, bidderResp)
	assert.Empty(t, errs)
}

func TestMakeBidsReturnsErrorOn400(t *testing.T) {
	adg := newTestAdapter(t)
	resp := &adapters.ResponseData{StatusCode: http.StatusBadRequest}
	bidderResp, errs := adg.MakeBids(&openrtb2.BidRequest{}, &adapters.RequestData{}, resp)
	assert.Nil(t, bidderResp)
	assert.Len(t, errs, 1)
	assert.IsType(t, &errortypes.BadInput{}, errs[0])
}

func TestMakeRequestsReturnsErrorWhenNoImp(t *testing.T) {
	adg := newTestAdapter(t)
	requests, errs := adg.MakeRequests(&openrtb2.BidRequest{ID: "test"}, &adapters.ExtraRequestInfo{})
	assert.Nil(t, requests)
	assert.Len(t, errs, 1)
	assert.IsType(t, &errortypes.BadInput{}, errs[0])
}

// unmarshalExtImpAdgeneration の各エラー分岐を網羅する。
func TestUnmarshalExtImpAdgenerationErrors(t *testing.T) {
	cases := []struct {
		name    string
		ext     json.RawMessage
		wantMsg string // 空なら任意のエラーで可
	}{
		{"imp.ext が不正 JSON", json.RawMessage(`not-json`), ""},
		{"bidder がオブジェクトでない", json.RawMessage(`{"bidder":"not-an-object"}`), ""},
		{"id が空文字", json.RawMessage(`{"bidder":{"id":""}}`), "No Location ID in ExtImpAdgeneration."},
		{"id キー欠落", json.RawMessage(`{"bidder":{"marginTop":"10"}}`), "No Location ID in ExtImpAdgeneration."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			imp := &openrtb2.Imp{ID: "imp-x", Ext: c.ext}
			adgExt, err := unmarshalExtImpAdgeneration(imp)
			assert.Nil(t, adgExt)
			assert.Error(t, err)
			if c.wantMsg != "" {
				assert.Equal(t, c.wantMsg, err.Error())
			}
		})
	}
}

// hasNativeAssets: ラップ/非ラップ + 不正入力の判定を網羅する (Prebid.js isNative 互換)。
func TestHasNativeAssets(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"非ラップ + assets あり", `{"assets":[{"id":1}]}`, true},
		{"ラップ + assets あり", `{"native":{"assets":[{"id":1}]}}`, true},
		{"assets 空配列", `{"assets":[]}`, false},
		{"assets キーなし", `{"link":{"url":"https://l.example/"}}`, false},
		{"不正 JSON", `not-json`, false},
		{"ラップ native がオブジェクトでない", `{"native":123}`, false},
		{"assets が配列でない", `{"assets":"foo"}`, false},
		{"ラップ assets が配列でない", `{"native":{"assets":"foo"}}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, hasNativeAssets(json.RawMessage(c.raw)))
		})
	}
}

// wrapNativeAdm のエラー分岐 (top/native/imptrackers の unmarshal 失敗) を網羅する。
func TestWrapNativeAdmErrors(t *testing.T) {
	// top の unmarshal 失敗
	_, err := wrapNativeAdm(json.RawMessage(`not-json`), "")
	assert.Error(t, err)
	// ラップ native がオブジェクトでない
	_, err = wrapNativeAdm(json.RawMessage(`{"native":123}`), "")
	assert.Error(t, err)
	// imptrackers が文字列配列でない (beaconUrl 追記時のみ到達)
	_, err = wrapNativeAdm(json.RawMessage(`{"assets":[{"id":1}],"imptrackers":"not-array"}`), "https://b.example/bc")
	assert.Error(t, err)
}

// buildAdMarkup: native adm 組み立てで wrapNativeAdm がエラーを返す経路。
func TestBuildAdMarkupNativeWrapError(t *testing.T) {
	adResult := &adgResult{
		Native:    json.RawMessage(`{"assets":[{"id":1}],"imptrackers":"not-array"}`),
		Beaconurl: "https://b.example/bc",
	}
	imp := &openrtb2.Imp{ID: "imp-native", Native: &openrtb2.Native{Request: `{}`}}
	_, _, err := buildAdMarkup(adResult, nil, imp)
	assert.Error(t, err)
}

// removeWrapper: <body> を含まない ad はそのまま返す (アンラップしない)。
func TestBuildAdMarkupBannerWithoutBodyTags(t *testing.T) {
	adResult := &adgResult{Ad: "plain-ad-no-body"}
	imp := &openrtb2.Imp{ID: "imp-1", Banner: &openrtb2.Banner{}}
	bidType, adm, err := buildAdMarkup(adResult, nil, imp)
	assert.NoError(t, err)
	assert.Equal(t, openrtb_ext.BidTypeBanner, bidType)
	assert.Equal(t, "plain-ad-no-body", adm)
}

// extractMarginTop: imp.ext が不正でも marginTop は空扱い ('0' に既定化) される。
func TestBuildAdMarkupUpperBillboardHandlesBadExt(t *testing.T) {
	adResult := &adgResult{Ad: "<!DOCTYPE html><body></body>", Vastxml: "<VAST/>"}
	loc := &adgLocationParams{Option: &adgLocationOption{AdType: "upper_billboard"}}
	imp := &openrtb2.Imp{ID: "imp-ub", Banner: &openrtb2.Banner{}, Ext: json.RawMessage(`not-json`)}
	_, adm, err := buildAdMarkup(adResult, loc, imp)
	assert.NoError(t, err)
	assert.Contains(t, adm, "marginTop: '0'")
}

// MakeBids: 500 は BadServerResponse を返す。
func TestMakeBidsReturnsServerErrorOn500(t *testing.T) {
	adg := newTestAdapter(t)
	resp := &adapters.ResponseData{StatusCode: http.StatusInternalServerError}
	bidderResp, errs := adg.MakeBids(&openrtb2.BidRequest{}, &adapters.RequestData{}, resp)
	assert.Nil(t, bidderResp)
	assert.Len(t, errs, 1)
	assert.IsType(t, &errortypes.BadServerResponse{}, errs[0])
}

// MakeBids: 200 でボディが不正 JSON の場合はエラー。
func TestMakeBidsReturnsErrorOnInvalidBody(t *testing.T) {
	adg := newTestAdapter(t)
	resp := &adapters.ResponseData{StatusCode: http.StatusOK, Body: []byte(`not-json`)}
	bidderResp, errs := adg.MakeBids(&openrtb2.BidRequest{}, &adapters.RequestData{Body: []byte(`{}`)}, resp)
	assert.Nil(t, bidderResp)
	assert.Len(t, errs, 1)
}

// MakeBids: externalRequest / sentBody にまつわるガード分岐を網羅する。
func TestMakeBidsExternalRequestGuards(t *testing.T) {
	adg := newTestAdapter(t)
	internal := &openrtb2.BidRequest{
		Imp: []openrtb2.Imp{{ID: "imp-1", Banner: &openrtb2.Banner{}, Ext: json.RawMessage(`{"bidder":{"id":"58278"}}`)}},
	}
	goodResp := func() *adapters.ResponseData {
		return &adapters.ResponseData{
			StatusCode: http.StatusOK,
			Body:       []byte(`{"locationid":"58278","results":[{"ad":"<body>x</body>","cpm":1}]}`),
		}
	}

	t.Run("externalRequest が nil", func(t *testing.T) {
		bidderResp, errs := adg.MakeBids(internal, nil, goodResp())
		assert.Nil(t, bidderResp)
		assert.Empty(t, errs)
	})
	t.Run("externalRequest.Body が空", func(t *testing.T) {
		bidderResp, errs := adg.MakeBids(internal, &adapters.RequestData{}, goodResp())
		assert.Nil(t, bidderResp)
		assert.Empty(t, errs)
	})
	t.Run("sentBody が不正 JSON", func(t *testing.T) {
		bidderResp, errs := adg.MakeBids(internal, &adapters.RequestData{Body: []byte(`not-json`)}, goodResp())
		assert.Nil(t, bidderResp)
		assert.Len(t, errs, 1)
	})
	t.Run("sentBody.Ortb に imp なし", func(t *testing.T) {
		sentBody, _ := json.Marshal(adgRequestBody{})
		bidderResp, errs := adg.MakeBids(internal, &adapters.RequestData{Body: sentBody}, goodResp())
		assert.Nil(t, bidderResp)
		assert.Empty(t, errs)
	})
	t.Run("sentBody の imp ID が internalRequest に存在しない", func(t *testing.T) {
		sentBody, _ := json.Marshal(adgRequestBody{Ortb: openrtb2.BidRequest{Imp: []openrtb2.Imp{{ID: "no-such-imp"}}}})
		bidderResp, errs := adg.MakeBids(internal, &adapters.RequestData{Body: sentBody}, goodResp())
		assert.Nil(t, bidderResp)
		assert.Empty(t, errs)
	})
}

// MakeBids: native adm 組み立てが失敗した場合はエラーを返す (buildAdMarkup 経由)。
func TestMakeBidsReturnsErrorWhenNativeAdmWrapFails(t *testing.T) {
	adg := newTestAdapter(t)
	internal := &openrtb2.BidRequest{
		Imp: []openrtb2.Imp{{ID: "imp-1", Native: &openrtb2.Native{Request: `{}`}, Ext: json.RawMessage(`{"bidder":{"id":"58278"}}`)}},
	}
	resp := &adapters.ResponseData{
		StatusCode: http.StatusOK,
		Body:       []byte(`{"locationid":"58278","results":[{"native":{"assets":[{"id":1}],"imptrackers":"not-array"},"beaconurl":"https://b.example/bc","cpm":10}]}`),
	}
	sentBody, _ := json.Marshal(adgRequestBody{Ortb: openrtb2.BidRequest{Imp: []openrtb2.Imp{{ID: "imp-1"}}}})
	bidderResp, errs := adg.MakeBids(internal, &adapters.RequestData{Body: sentBody}, resp)
	assert.Nil(t, bidderResp)
	assert.Len(t, errs, 1)
}
