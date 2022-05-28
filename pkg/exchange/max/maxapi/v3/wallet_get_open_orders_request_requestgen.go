// Code generated by "requestgen -method GET -url /api/v3/wallet/:walletType/orders/open -type WalletGetOpenOrdersRequest -responseType []Order"; DO NOT EDIT.

package v3

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/c9s/bbgo/pkg/exchange/max/maxapi"
	"net/url"
	"reflect"
	"regexp"
)

func (w *WalletGetOpenOrdersRequest) Market(market string) *WalletGetOpenOrdersRequest {
	w.market = market
	return w
}

func (w *WalletGetOpenOrdersRequest) WalletType(walletType max.WalletType) *WalletGetOpenOrdersRequest {
	w.walletType = walletType
	return w
}

// GetQueryParameters builds and checks the query parameters and returns url.Values
func (w *WalletGetOpenOrdersRequest) GetQueryParameters() (url.Values, error) {
	var params = map[string]interface{}{}

	query := url.Values{}
	for _k, _v := range params {
		query.Add(_k, fmt.Sprintf("%v", _v))
	}

	return query, nil
}

// GetParameters builds and checks the parameters and return the result in a map object
func (w *WalletGetOpenOrdersRequest) GetParameters() (map[string]interface{}, error) {
	var params = map[string]interface{}{}
	// check market field -> json key market
	market := w.market

	// TEMPLATE check-required
	if len(market) == 0 {
		return nil, fmt.Errorf("market is required, empty string given")
	}
	// END TEMPLATE check-required

	// assign parameter of market
	params["market"] = market

	return params, nil
}

// GetParametersQuery converts the parameters from GetParameters into the url.Values format
func (w *WalletGetOpenOrdersRequest) GetParametersQuery() (url.Values, error) {
	query := url.Values{}

	params, err := w.GetParameters()
	if err != nil {
		return query, err
	}

	for _k, _v := range params {
		if w.isVarSlice(_v) {
			w.iterateSlice(_v, func(it interface{}) {
				query.Add(_k+"[]", fmt.Sprintf("%v", it))
			})
		} else {
			query.Add(_k, fmt.Sprintf("%v", _v))
		}
	}

	return query, nil
}

// GetParametersJSON converts the parameters from GetParameters into the JSON format
func (w *WalletGetOpenOrdersRequest) GetParametersJSON() ([]byte, error) {
	params, err := w.GetParameters()
	if err != nil {
		return nil, err
	}

	return json.Marshal(params)
}

// GetSlugParameters builds and checks the slug parameters and return the result in a map object
func (w *WalletGetOpenOrdersRequest) GetSlugParameters() (map[string]interface{}, error) {
	var params = map[string]interface{}{}
	// check walletType field -> json key walletType
	walletType := w.walletType

	// TEMPLATE check-required
	if len(walletType) == 0 {
		return nil, fmt.Errorf("walletType is required, empty string given")
	}
	// END TEMPLATE check-required

	// assign parameter of walletType
	params["walletType"] = walletType

	return params, nil
}

func (w *WalletGetOpenOrdersRequest) applySlugsToUrl(url string, slugs map[string]string) string {
	for _k, _v := range slugs {
		needleRE := regexp.MustCompile(":" + _k + "\\b")
		url = needleRE.ReplaceAllString(url, _v)
	}

	return url
}

func (w *WalletGetOpenOrdersRequest) iterateSlice(slice interface{}, _f func(it interface{})) {
	sliceValue := reflect.ValueOf(slice)
	for _i := 0; _i < sliceValue.Len(); _i++ {
		it := sliceValue.Index(_i).Interface()
		_f(it)
	}
}

func (w *WalletGetOpenOrdersRequest) isVarSlice(_v interface{}) bool {
	rt := reflect.TypeOf(_v)
	switch rt.Kind() {
	case reflect.Slice:
		return true
	}
	return false
}

func (w *WalletGetOpenOrdersRequest) GetSlugsMap() (map[string]string, error) {
	slugs := map[string]string{}
	params, err := w.GetSlugParameters()
	if err != nil {
		return slugs, nil
	}

	for _k, _v := range params {
		slugs[_k] = fmt.Sprintf("%v", _v)
	}

	return slugs, nil
}

func (w *WalletGetOpenOrdersRequest) Do(ctx context.Context) ([]max.Order, error) {

	// empty params for GET operation
	var params interface{}
	query, err := w.GetParametersQuery()
	if err != nil {
		return nil, err
	}

	apiURL := "/api/v3/wallet/:walletType/orders/open"
	slugs, err := w.GetSlugsMap()
	if err != nil {
		return nil, err
	}

	apiURL = w.applySlugsToUrl(apiURL, slugs)

	req, err := w.client.NewAuthenticatedRequest(ctx, "GET", apiURL, query, params)
	if err != nil {
		return nil, err
	}

	response, err := w.client.SendRequest(req)
	if err != nil {
		return nil, err
	}

	var apiResponse []max.Order
	if err := response.DecodeJSON(&apiResponse); err != nil {
		return nil, err
	}
	return apiResponse, nil
}