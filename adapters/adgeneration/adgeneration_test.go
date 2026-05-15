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
	assert.Equal(t, "1.0.3", body.Adapterver)
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

func TestGetCurrency(t *testing.T) {
	adg := newTestAdapter(t)
	cases := []struct {
		name string
		cur  []string
		want string
	}{
		{"default when empty", nil, "JPY"},
		{"prefers JPY when supported", []string{"USD", "JPY"}, "JPY"},
		{"falls back to first when JPY missing", []string{"USD", "EUR"}, "USD"},
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

	bidderResp, errs := adg.MakeBids(internalRequest, &adapters.RequestData{}, resp)
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
}
