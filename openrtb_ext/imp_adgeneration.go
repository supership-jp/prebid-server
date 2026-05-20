package openrtb_ext

type ExtImpAdgeneration struct {
	Id string `json:"id"`
	// MarginTop は upper_billboard 配置で ADGBrowserM.init({marginTop}) に渡す値。
	// Prebid.js 側 (bidder params.marginTop) と挙動を揃えるための任意項目。
	MarginTop string `json:"marginTop,omitempty"`
}
