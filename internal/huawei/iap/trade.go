package iap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// TradeOrder is the normalized subset of one record returned by Huawei's
// application purchase-record query. Alternate field names are retained because
// Huawei has used requestId/orderNo in older response models and purchaseOrderId
// in HarmonyOS order payloads.
type TradeOrder struct {
	PurchaseOrderID  string          `json:"purchaseOrderId"`
	RequestID        string          `json:"requestId"`
	OrderNo          string          `json:"orderNo"`
	PurchaseToken    string          `json:"purchaseToken"`
	ProductID        string          `json:"productId"`
	ProductNo        string          `json:"productNo"`
	ProductType      FlexibleInt     `json:"productType"`
	TradeType        string          `json:"tradeType"`
	TradeTime        Millis          `json:"tradeTime"`
	PurchaseTime     Millis          `json:"purchaseTime"`
	DeveloperPayload string          `json:"developerPayload"`
	Environment      string          `json:"environment"`
	Price            json.Number     `json:"price"`
	Currency         string          `json:"currency"`
	Raw              json.RawMessage `json:"-"`
}

func (o TradeOrder) EffectiveOrderID() string {
	if o.PurchaseOrderID != "" {
		return o.PurchaseOrderID
	}
	if o.RequestID != "" {
		return o.RequestID
	}
	return o.OrderNo
}

func (o TradeOrder) EffectiveProductID() string {
	if o.ProductID != "" {
		return o.ProductID
	}
	return o.ProductNo
}

func (o TradeOrder) OccurredAt() time.Time {
	if int64(o.TradeTime) > 0 {
		return o.TradeTime.Time()
	}
	return o.PurchaseTime.Time()
}

type TradeOrdersPage struct {
	Orders            []TradeOrder
	ContinuationToken string
}

func (c *Client) QueryTradeOrders(
	ctx context.Context,
	start time.Time,
	end time.Time,
	continuationToken string,
) (TradeOrdersPage, error) {
	if !end.After(start) || end.Sub(start) > 48*time.Hour {
		return TradeOrdersPage{}, errors.New("Huawei trade query window must be greater than zero and at most 48 hours")
	}
	body := map[string]interface{}{
		"startTime": start.UTC().UnixMilli(),
		"endTime":   end.UTC().Add(-time.Millisecond).UnixMilli(),
	}
	if continuationToken != "" {
		body["continuationToken"] = continuationToken
	}
	var response struct {
		ResponseCode      string          `json:"responseCode"`
		ResponseMessage   string          `json:"responseMessage"`
		ContinuationToken string          `json:"continuationToken"`
		OrderInfoList     json.RawMessage `json:"orderInfoList"`
		TradeOrderList    json.RawMessage `json:"tradeOrderList"`
		Orders            json.RawMessage `json:"orders"`
	}
	if err := c.callJSON(ctx, tradeOrdersQueryPath, body, &response); err != nil {
		return TradeOrdersPage{}, err
	}
	if response.ResponseCode != "0" {
		return TradeOrdersPage{}, fmt.Errorf(
			"Huawei trade order query failed: code=%s message=%s",
			response.ResponseCode,
			response.ResponseMessage,
		)
	}
	rawOrders := response.OrderInfoList
	if len(rawOrders) == 0 || string(rawOrders) == "null" {
		rawOrders = response.TradeOrderList
	}
	if len(rawOrders) == 0 || string(rawOrders) == "null" {
		rawOrders = response.Orders
	}
	page := TradeOrdersPage{ContinuationToken: response.ContinuationToken}
	if len(rawOrders) == 0 || string(rawOrders) == "null" {
		return page, nil
	}
	var snapshots []json.RawMessage
	if err := json.Unmarshal(rawOrders, &snapshots); err != nil {
		return TradeOrdersPage{}, fmt.Errorf("decode Huawei trade orders: %w", err)
	}
	page.Orders = make([]TradeOrder, 0, len(snapshots))
	for _, snapshot := range snapshots {
		var order TradeOrder
		if err := json.Unmarshal(snapshot, &order); err != nil {
			return TradeOrdersPage{}, fmt.Errorf("decode Huawei trade order: %w", err)
		}
		order.Raw = append(json.RawMessage(nil), snapshot...)
		if order.EffectiveOrderID() == "" || order.TradeType == "" {
			return TradeOrdersPage{}, errors.New("Huawei trade order is missing order identity or tradeType")
		}
		page.Orders = append(page.Orders, order)
	}
	return page, nil
}
