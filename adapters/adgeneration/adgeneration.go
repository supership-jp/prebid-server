package adgeneration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/golang/glog"
	"github.com/mxmCherry/openrtb"
	"github.com/prebid/prebid-server/adapters"
	"github.com/prebid/prebid-server/errortypes"
	"github.com/prebid/prebid-server/openrtb_ext"
)

type AdgenerationAdapter struct {
	endpoint        string
	version         string
	defaultCurrency string
}

// Server Responses
type adgServerResponseResult struct {
	Locationid string              `json:"locationid"`
	Results    []adgServerResponse `json:"results"`
}
type adgServerResponse struct {
	Ad         string      `json:"ad"`
	Beacon     string      `json:"beacon"`
	Beaconurl  string      `json:"beaconurl"`
	Cpm        float64     `json:"cpm"`
	Creativeid string      `json:"creativeid"`
	H          uint64      `json:"h"`
	W          uint64      `json:"w"`
	Ttl        uint64      `json:"ttl"`
	Vastxml    string      `json:"vastxml,omitempty"`
	NativeAd   adgNativeAd `json:"native_ad,omitempty"`
	LandingUrl string      `json:"landing_url"`
	Scheduleid string      `json:"scheduleid"`
	Trackers   adgTrackers `json:"trackers"`
}
type adgTrackers struct {
	Imp              []string `json:"imp"`
	ViewableImp      []string `json:"viewable_imp"`
	ViewableMeasured []string `json:"viewable_measured"`
}

type adgNativeAd struct {
	Assets      []adgNativeAsset `json:"assets"`
	Imptrackers []string         `json:"imptrackers"`
	Link        struct {
		Clicktrackers []string `json:"clicktrackers"`
		Url           string   `json:"url"`
	} `json:"link"`
	Jstracker string `json:"jstracker"`
}

type admNative struct {
	Title string `json:"title"`
	Image struct {
		Width  uint64 `json:"width"`
		Height uint64 `json:"height"`
		Url    string `json:"url"`
	} `json:"image"`
	Icon struct {
		Width  uint64 `json:"width"`
		Height uint64 `json:"height"`
		Url    string `json:"url"`
	} `json:"icon"`
	SponsoredBy        string   `json:"sponsoredBy"`
	Body               string   `json:"body"`
	Cta                string   `json:"cta"`
	PrivacyLink        string   `json:"privacyLink"`
	ClickUrl           string   `json:"clickUrl"`
	ClickTrackers      []string `json:"clickTrackers"`
	ImpressionTrackers []string `json:"impremessonTrackers"`
}

type adgNativeAsset struct {
	Id   uint64 `json:"id"`
	Data struct {
		Ext struct {
			BlackBack string `json:"black_back"`
		}
		Label string `json:"label"`
		Value string `json:"value"`
	} `json:"data,omitempty"`
	Required bool `json:"required,omitempty"`
	Img      struct {
		H   uint64 `json:"h"`
		W   uint64 `json:"w"`
		Url string `json:"url"`
	} `json:"img,omitempty"`
	Title struct {
		Text string `json:"title"`
	} `json:"title,omitempty"`
}

func (adg *AdgenerationAdapter) MakeRequests(request *openrtb.BidRequest, reqInfo *adapters.ExtraRequestInfo) ([]*adapters.RequestData, []error) {
	numRequests := len(request.Imp)
	var errs []error

	glog.Errorf(fmt.Sprintf("%#v", request))

	if numRequests == 0 {
		errs = append(errs, &errortypes.BadInput{
			Message: "No impression in the bid request",
		})
		return nil, errs
	}

	requestArray := make([]*adapters.RequestData, 0, numRequests)
	for index := 0; index < numRequests; index++ {
		headers := http.Header{}
		headers.Add("Content-Type", "application/json;charset=utf-8")
		headers.Add("Accept", "application/json")

		requestUri, err := adg.getRequestUri(request, index)
		if err != nil {
			errs = append(errs, err)
			return nil, errs
		}
		glog.Error(requestUri)
		request := &adapters.RequestData{
			Method:  "GET",
			Uri:     requestUri,
			Body:    nil,
			Headers: headers,
		}
		requestArray = append(requestArray, request)
	}

	return requestArray, errs
}

func (adg *AdgenerationAdapter) getRequestUri(request *openrtb.BidRequest, index int) (string, error) {
	imp := request.Imp[index]
	bidderExt, err := adg.unmarshalExtImpBidder(&imp)
	if err != nil {
		return "", &errortypes.BadInput{
			Message: err.Error(),
		}
	}
	adgExt, err := adg.unmarshalExtImpAdgeneration(&bidderExt)
	if err != nil {
		return "", &errortypes.BadInput{
			Message: err.Error(),
		}
	}

	endpoint := adg.endpoint +
		"?posall=SSPLOC" +
		"&id=" + adgExt.Id +
		"&sdktype=0" +
		"&hb=true" +
		"&t=json3" +
		"&sizes=" + adg.getSizes(&imp) +
		"&currency=" + adg.getCurrency(request) +
		"&sdkname=prebidserver" +
		"&tp=" + request.Site.Page +
		"&adapterver=" + adg.version
	if imp.Native == nil {
		endpoint += "&imark=1"
	}

	return endpoint, nil
}

func (adg *AdgenerationAdapter) unmarshalExtImpBidder(imp *openrtb.Imp) (adapters.ExtImpBidder, error) {
	var bidderExt adapters.ExtImpBidder
	if err := json.Unmarshal(imp.Ext, &bidderExt); err != nil {
		return bidderExt, err
	}
	return bidderExt, nil
}

func (adg *AdgenerationAdapter) unmarshalExtImpAdgeneration(ext *adapters.ExtImpBidder) (openrtb_ext.ExtImpAdgeneration, error) {
	var adgExt openrtb_ext.ExtImpAdgeneration
	if err := json.Unmarshal(ext.Bidder, &adgExt); err != nil {
		return adgExt, err
	}
	return adgExt, nil
}

func (adg *AdgenerationAdapter) getSizes(imp *openrtb.Imp) string {
	if imp.Banner == nil && len(imp.Banner.Format) == 0 {
		return ""
	}
	var sizeStr string
	for _, v := range imp.Banner.Format {
		sizeStr += strconv.FormatUint(v.W, 10) + "×" + strconv.FormatUint(v.H, 10) + ","
	}
	if len(sizeStr) > 0 && strings.LastIndex(sizeStr, ",") == len(sizeStr)-1 {
		sizeStr = sizeStr[:len(sizeStr)-1]
	}
	return sizeStr
}

func (adg *AdgenerationAdapter) getCurrency(request *openrtb.BidRequest) string {
	if len(request.Cur) <= 0 {
		return adg.defaultCurrency
	} else {
		for _, c := range request.Cur {
			if adg.defaultCurrency == c {
				return c
			}
		}
		return request.Cur[0]
	}
}

func (adg *AdgenerationAdapter) MakeBids(internalRequest *openrtb.BidRequest, externalRequest *adapters.RequestData, response *adapters.ResponseData) (*adapters.BidderResponse, []error) {
	// var errs []error
	if response.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if response.StatusCode == http.StatusBadRequest {
		return nil, []error{&errortypes.BadInput{
			Message: fmt.Sprintf("unexpected status code: %d. Run with request.debug = 1 for more info", response.StatusCode),
		}}
	}
	if response.StatusCode != http.StatusOK {
		return nil, []error{&errortypes.BadServerResponse{
			Message: fmt.Sprintf("unexpected status code: %d. Run with request.debug = 1 for more info", response.StatusCode),
		}}
	}
	var bidResp adgServerResponseResult
	err := json.Unmarshal(response.Body, &bidResp)
	if err != nil {
		return nil, []error{err}
	}
	if len(bidResp.Results) <= 0 {
		return nil, nil
	}

	bidResponse := adapters.NewBidderResponseWithBidsCapacity(len(bidResp.Results))
	var impId string
	for _, result := range bidResp.Results {
		var bitType openrtb_ext.BidType
		var adm string
		for _, v := range internalRequest.Imp {
			bidderExt, err := adg.unmarshalExtImpBidder(&v)
			if err != nil {
				return nil, []error{&errortypes.BadServerResponse{
					Message: err.Error(),
				},
				}
			}
			adgExt, err := adg.unmarshalExtImpAdgeneration(&bidderExt)
			if err != nil {
				return nil, []error{&errortypes.BadServerResponse{
					Message: err.Error(),
				},
				}
			}
			if adgExt.Id == bidResp.Locationid {
				impId = v.ID
			}
		}

		if adg.isNative(&result) {
			bitType = openrtb_ext.BidTypeNative
			adm, err = adg.createNativeAd(&result)
			if err != nil {
				return nil, []error{&errortypes.BadServerResponse{
					Message: err.Error(),
				},
				}
			}
		} else {
			bitType = openrtb_ext.BidTypeBanner
			adm = adg.createAd(&result, impId)
		}

		bid := openrtb.Bid{
			ID:    bidResp.Locationid,
			ImpID: impId,
			AdM:   adm,
			Price: result.Cpm,
			W:     result.W,
			H:     result.H,
			CrID:  result.Creativeid,
		}

		bidResponse.Bids = append(bidResponse.Bids, &adapters.TypedBid{
			Bid:     &bid,
			BidType: bitType,
		})
	}
	return bidResponse, nil
}

func (adg *AdgenerationAdapter) isNative(body *adgServerResponse) bool {
	if body == nil {
		return false
	}
	return &body.NativeAd != nil && len(body.NativeAd.Assets) > 0
}

func (adg *AdgenerationAdapter) createNativeAd(body *adgServerResponse) (string, error) {
	native := admNative{}
	if &body.NativeAd != nil && len(body.NativeAd.Assets) > 0 {
		assets := body.NativeAd.Assets
		for _, v := range assets {
			switch v.Id {
			case 1:
				native.Title = v.Title.Text
			case 2:
				native.Image.Url = v.Img.Url
				native.Image.Height = v.Img.H
				native.Image.Width = v.Img.W
			case 3:
				native.Icon.Url = v.Img.Url
				native.Icon.Height = v.Img.H
				native.Icon.Width = v.Img.W
			case 4:
				native.SponsoredBy = v.Data.Value
			case 5:
				native.Body = v.Data.Value
			case 6:
				native.Cta = v.Data.Value
			case 502:
				native.PrivacyLink = url.PathEscape(v.Data.Value)
			}
		}
		native.ClickUrl = body.NativeAd.Link.Url
		native.ClickTrackers = body.NativeAd.Link.Clicktrackers
		native.ImpressionTrackers = body.NativeAd.Imptrackers
		if body.Beaconurl != "" {
			native.ImpressionTrackers = append(native.ImpressionTrackers, body.Beaconurl)
		}
	}
	admJson, err := json.Marshal(native)
	if err != nil {
		return "", err
	}
	return string(admJson), nil
}

func (adg *AdgenerationAdapter) createAd(body *adgServerResponse, impId string) string {
	ad := body.Ad
	if body.Vastxml != "" {
		ad = "<body><div id=\"apvad-" + impId + "\"></div>" + adg.createAPVTag() + adg.insertVASTMethod(impId, body.Vastxml) + "</body>"
	}
	ad = adg.appendChildToBody(ad, body.Beacon)
	if adg.removeWrapper(ad) != "" {
		return adg.removeWrapper(ad)
	}
	return ad
}

func (adg *AdgenerationAdapter) createAPVTag() string {
	return "<script type=\"text/javascript\" id=\"apv\" src=\"https://cdn.apvdr.com/js/VideoAd.min.js\"></script>"
}

func (adg *AdgenerationAdapter) insertVASTMethod(bidId string, vastxml string) string {
	rep := regexp.MustCompile(`/\r?\n/g`)
	var replacedVastxml = rep.ReplaceAllString(vastxml, "")
	return "<script type=\"text/javascript\"> (function(){ new APV.VideoAd({s:" + bidId + "}).load('" + replacedVastxml + "'); })(); </script>"
}

func (adg *AdgenerationAdapter) appendChildToBody(ad string, data string) string {
	rep := regexp.MustCompile(`<\/\s?body>`)
	return rep.ReplaceAllString(ad, data+"</body>")
}

func (adg *AdgenerationAdapter) removeWrapper(ad string) string {
	bodyIndex := strings.Index(ad, "<body>")
	lastBodyIndex := strings.LastIndex(ad, "</body>")
	if bodyIndex == -1 || lastBodyIndex == -1 {
		return ""
	}
	str := strings.Replace(strings.Replace(ad[bodyIndex:lastBodyIndex], "<body>", "", 1), "</body>", "", 1)
	return str
}

func NewAdgenerationAdapter(endpoint string) *AdgenerationAdapter {
	return &AdgenerationAdapter{
		endpoint,
		"0.0.1",
		"USD",
	}
}
