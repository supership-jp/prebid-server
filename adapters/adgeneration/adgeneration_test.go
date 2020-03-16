package adgeneration

import (
	"testing"

	"github.com/prebid/prebid-server/adapters/adapterstest"
)

func TestJsonSamples(t *testing.T) {
	adapterstest.RunJSONBidderTest(t, "adgenerationtest", NewAdgenerationAdapter("https://d.socdm.com/adsv/v1"))
}

// func TestGetRequestUri(t *testing.T) {
// 	bidder := NewAdgenerationAdapter("https://d.socdm.com/adsv/v1")
// 	request := &openrtb.BidRequest{
// 		ID: "test-bid-request",
// 		Imp: []openrtb.Imp{
// 			{ID: "ExtImpBidder-failed-test", Banner: &openrtb.Banner{Format: []openrtb.Format{{W: 300, H: 250}, {W: 320, H: 50}}}, Ext: json.RawMessage(`{"_bidder": { "id": "32344" }}`)},
// 			{ID: "ExtImpAdgeneration-failed-test", Banner: &openrtb.Banner{Format: []openrtb.Format{{W: 300, H: 250}, {W: 320, H: 50}}}, Ext: json.RawMessage(`{"bidder": { "_id": "32344" }}`)},
// 			{ID: "incorrect-bidder-field", Banner: &openrtb.Banner{}, Ext: json.RawMessage(`{"bidder": { "id": "32344" }}`)},
// 			{ID: "-field", Banner: &openrtb.Banner{Format: []openrtb.Format{{W: 300, H: 250}, {W: 320, H: 50}}}, Ext: json.RawMessage(`{"bidder": { "id": "32344" }}`)},
// 		},
// 		Device: &openrtb.Device{UA: "testUA", IP: "testIP"},
// 		User:   &openrtb.User{BuyerUID: "buyerID"},
// 	}

// 	httpRequests, errs := bidder.MakeRequests(request, &adapters.ExtraRequestInfo{})
// 	if len(errs) > 0 {
// 		// TODO: 文言を帰る
// 		t.Errorf("Got unexpected errors while building HTTP requests: %v", errs)
// 	}
// 	if len(httpRequests) != 1 {
// 		// TODO: 文言を帰る
// 		t.Fatalf("Unexpected number of HTTP requests. Got %d. Expected %d", len(httpRequests), 1)
// 	}

// }

// func TestGetSizes(t *testing.T) {

// }

// func getCurrency(t *testing.T) {

// }
