// Package clawallex provides a client for the Clawallex Payment API.
package clawallex

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ─── Errors ───────────────────────────────────────────────────────────────────

// APIError is returned for non-2xx responses (except 402).
type APIError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("clawallex: %d %s — %s", e.StatusCode, e.Code, e.Message)
}

// PaymentRequiredError is returned when a Mode B card order responds with
// HTTP 402. Details contains the payment challenge fields (payee_address,
// asset_address, payable_amount, x402_reference_id, fee breakdown, etc.).
type PaymentRequiredError struct {
	Code    string
	Message string
	Details CardOrder402Details
}

func (e *PaymentRequiredError) Error() string {
	return fmt.Sprintf("clawallex: 402 %s — %s", e.Code, e.Message)
}

// ─── Constants ────────────────────────────────────────────────────────────────

// ModeCode specifies the funding source for card creation.
const (
	ModeWallet = 100 // Mode A: deduct from wallet balance
	ModeX402   = 200 // Mode B: on-chain x402 USDC payment
)

// CardType specifies the card lifecycle.
const (
	Flash  = 100 // One-time use, auto-destroyed after a single transaction
	Stream = 200 // Reloadable, suitable for recurring payments
)

// ─── Types ────────────────────────────────────────────────────────────────────

type WalletDetail struct {
	WalletID             string `json:"wallet_id"`
	WalletType           int    `json:"wallet_type"`
	Currency             string `json:"currency"`
	AvailableBalance     string `json:"available_balance"`
	FrozenBalance        string `json:"frozen_balance"`
	LowBalanceThreshold  string `json:"low_balance_threshold"`
	Status               int    `json:"status"`
	UpdatedAt            string `json:"updated_at"`
}

type RechargeAddress struct {
	RechargeAddressID string `json:"recharge_address_id"`
	WalletID          string `json:"wallet_id"`
	ChainCode         string `json:"chain_code"`
	TokenCode         string `json:"token_code"`
	Address           string `json:"address"`
	MemoTag           string `json:"memo_tag"`
	Status            int    `json:"status"`
	UpdatedAt         string `json:"updated_at"`
}

type RechargeAddressesResponse struct {
	WalletID string            `json:"wallet_id"`
	Total    int               `json:"total"`
	Data     []RechargeAddress `json:"data"`
}

type PayeeAddressResponse struct {
	ChainCode string `json:"chain_code"`
	TokenCode string `json:"token_code"`
	Address   string `json:"address"`
}

type AssetAddressResponse struct {
	ChainCode    string `json:"chain_code"`
	TokenCode    string `json:"token_code"`
	AssetAddress string `json:"asset_address"`
}

// ─── x402 / EIP-3009 payload types ───────────────────────────────────────────

// X402Authorization holds the EIP-3009 transferWithAuthorization fields.
// See https://eips.ethereum.org/EIPS/eip-3009
type X402Authorization struct {
	From        string `json:"from"`        // Agent wallet address (payer)
	To          string `json:"to"`          // Must equal 402 payee_address
	Value       string `json:"value"`       // payable_amount × 10^decimals (USDC=6, e.g. "207590000")
	ValidAfter  string `json:"validAfter"`  // Unix seconds, recommended now - 60
	ValidBefore string `json:"validBefore"` // Unix seconds, recommended now + 3600
	Nonce       string `json:"nonce"`       // Random 32-byte hex with 0x prefix
}

// X402PaymentPayload wraps the EIP-3009 signature and authorization.
type X402PaymentPayload struct {
	Scheme  string `json:"scheme"`  // Fixed "exact"
	Network string `json:"network"` // "ETH" / "BASE"
	Payload struct {
		Signature     string           `json:"signature"`     // EIP-3009 typed-data signature hex
		Authorization X402Authorization `json:"authorization"`
	} `json:"payload"`
}

// X402PaymentRequirements describes what the payment must satisfy.
type X402PaymentRequirements struct {
	Scheme           string `json:"scheme"`           // Fixed "exact"
	Network          string `json:"network"`          // Must equal PaymentPayload.Network
	Asset            string `json:"asset"`            // Token contract — must equal 402 asset_address
	PayTo            string `json:"payTo"`            // Must equal 402 payee_address and Authorization.To
	MaxAmountRequired string `json:"maxAmountRequired"` // Must equal Authorization.Value
	Extra            struct {
		ReferenceID string `json:"referenceId"` // Must equal 402 x402_reference_id
	} `json:"extra"`
}

// ToMap converts X402PaymentPayload to map[string]any for use in NewCardParams.
func (p X402PaymentPayload) ToMap() map[string]any {
	return map[string]any{
		"scheme": p.Scheme, "network": p.Network,
		"payload": map[string]any{
			"signature": p.Payload.Signature,
			"authorization": map[string]any{
				"from": p.Payload.Authorization.From, "to": p.Payload.Authorization.To,
				"value": p.Payload.Authorization.Value,
				"validAfter": p.Payload.Authorization.ValidAfter,
				"validBefore": p.Payload.Authorization.ValidBefore,
				"nonce": p.Payload.Authorization.Nonce,
			},
		},
	}
}

// ToMap converts X402PaymentRequirements to map[string]any for use in NewCardParams.
func (r X402PaymentRequirements) ToMap() map[string]any {
	return map[string]any{
		"scheme": r.Scheme, "network": r.Network,
		"asset": r.Asset, "payTo": r.PayTo,
		"maxAmountRequired": r.MaxAmountRequired,
		"extra": map[string]any{"referenceId": r.Extra.ReferenceID},
	}
}

// ─── Cards ───────────────────────────────────────────────────────────────────

type NewCardParams struct {
	ModeCode                int                    `json:"mode_code"`
	CardType                int                    `json:"card_type"`
	Amount                  string                 `json:"amount"`
	ClientRequestID         string                 `json:"client_request_id"`
	FeeAmount               string                 `json:"fee_amount,omitempty"`
	IssuerCardCurrency      string                 `json:"issuer_card_currency,omitempty"`
	TxLimit                 string                 `json:"tx_limit,omitempty"`
	AllowedMCC              string                 `json:"allowed_mcc,omitempty"`
	BlockedMCC              string                 `json:"blocked_mcc,omitempty"`
	ChainCode               string                 `json:"chain_code,omitempty"`
	TokenCode               string                 `json:"token_code,omitempty"`
	X402ReferenceID         string                 `json:"x402_reference_id,omitempty"`
	X402Version             int                    `json:"x402_version,omitempty"`
	PaymentPayload          map[string]any         `json:"payment_payload,omitempty"`
	PaymentRequirements     map[string]any         `json:"payment_requirements,omitempty"`
	Extra                   map[string]string      `json:"extra,omitempty"`
	PayerAddress            string                 `json:"payer_address,omitempty"`
}

// CardOrder402Details holds the payment challenge returned with HTTP 402
// during a Mode B card order first request.
type CardOrder402Details struct {
	CardOrderID       string `json:"card_order_id"`
	ClientRequestID   string `json:"client_request_id"`
	X402ReferenceID   string `json:"x402_reference_id"`
	PayeeAddress      string `json:"payee_address"`
	AssetAddress      string `json:"asset_address"`
	FinalCardAmount   string `json:"final_card_amount"`
	IssueFeeAmount    string `json:"issue_fee_amount"`
	MonthlyFeeAmount  string `json:"monthly_fee_amount"`
	FxFeeAmount       string `json:"fx_fee_amount"`
	FeeAmount         string `json:"fee_amount"`
	PayableAmount     string `json:"payable_amount"`
}

type CardOrderResponse struct {
	CardOrderID string `json:"card_order_id"`
	CardID      string `json:"card_id"`
	Status      int    `json:"status"`
	ReferenceID string `json:"reference_id,omitempty"`
	Idempotent  bool   `json:"idempotent,omitempty"`
}

type CardListParams struct {
	Page     int
	PageSize int
}

type Card struct {
	CardID           string `json:"card_id"`
	ModeCode         int    `json:"mode_code"`
	CardType         int    `json:"card_type"`
	Status           int    `json:"status"`
	MaskedPAN        string `json:"masked_pan"`
	CardCurrency     string `json:"card_currency"`
	AvailableBalance string `json:"available_balance"`
	ExpiryMonth      int    `json:"expiry_month"`
	ExpiryYear       int    `json:"expiry_year"`
	IssuerCardStatus string `json:"issuer_card_status"`
	UpdatedAt        string `json:"updated_at"`
}

type CardListResponse struct {
	Total    int    `json:"total"`
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
	Data     []Card `json:"data"`
}

type CardBalanceResponse struct {
	CardID           string `json:"card_id"`
	CardCurrency     string `json:"card_currency"`
	AvailableBalance string `json:"available_balance"`
	Status           int    `json:"status"`
	UpdatedAt        string `json:"updated_at"`
}

type EncryptedSensitiveData struct {
	Version    string `json:"version"`
	Algorithm  string `json:"algorithm"`
	KDF        string `json:"kdf"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

type CardDetailsResponse struct {
	CardID                 string                 `json:"card_id"`
	MaskedPAN              string                 `json:"masked_pan"`
	EncryptedSensitiveData EncryptedSensitiveData `json:"encrypted_sensitive_data"`
	ExpiryMonth            int                    `json:"expiry_month"`
	ExpiryYear             int                    `json:"expiry_year"`
	TxLimit                string                 `json:"tx_limit"`
	AllowedMCC             string                 `json:"allowed_mcc"`
	BlockedMCC             string                 `json:"blocked_mcc"`
	CardCurrency           string                 `json:"card_currency"`
	AvailableBalance       string                 `json:"available_balance"`
	FirstName              string                 `json:"first_name"`
	LastName               string                 `json:"last_name"`
	DeliveryAddress        string                 `json:"delivery_address"` // billing address JSON or text
	Status                 int                    `json:"status"`
	IssuerCardStatus       string                 `json:"issuer_card_status"`
	UpdatedAt              string                 `json:"updated_at"`
}

type TransactionListParams struct {
	CardTxID    string
	IssuerTxID  string
	CardID      string
	Page        int
	PageSize    int
}

type Transaction struct {
	CardID                    string  `json:"card_id"`
	CardTxID                  string  `json:"card_tx_id"`
	IssuerTxID                string  `json:"issuer_tx_id"`
	IssuerOriTxID             string  `json:"issuer_ori_tx_id"`
	ActionType                int     `json:"action_type"`
	TxType                    int     `json:"tx_type"`
	ProcessStatus             string  `json:"process_status"`
	Amount                    string  `json:"amount"`
	FeeAmount                 string  `json:"fee_amount"`
	FeeCurrency               string  `json:"fee_currency"`
	BillingAmount             string  `json:"billing_amount"`
	BillingCurrency           string  `json:"billing_currency"`
	TransactionAmount         string  `json:"transaction_amount"`
	TransactionCurrency       string  `json:"transaction_currency"`
	Status                    int     `json:"status"`
	CardFundApplied           int     `json:"card_fund_applied"`
	IsInProgress              int     `json:"is_in_progress"`
	MerchantName              string  `json:"merchant_name"`
	MCC                       string  `json:"mcc"`
	DeclineReason             string  `json:"decline_reason"`
	Description               string  `json:"description"`
	IssuerCardAvailableBalance string `json:"issuer_card_available_balance"`
	OccurredAt                string  `json:"occurred_at"`
	SettledAt                 *string `json:"settled_at"`
	WebhookEventID            string  `json:"webhook_event_id"`
}

type TransactionListResponse struct {
	CardTxID   string        `json:"card_tx_id"`
	IssuerTxID string        `json:"issuer_tx_id"`
	CardID     string        `json:"card_id"`
	Page       int           `json:"page"`
	PageSize   int           `json:"page_size"`
	Total      int           `json:"total"`
	Data       []Transaction `json:"data"`
}

type UpdateCardParams struct {
	ClientRequestID string `json:"client_request_id"`
	TxLimit         string `json:"tx_limit,omitempty"`
	AllowedMCC      string `json:"allowed_mcc,omitempty"`
	BlockedMCC      string `json:"blocked_mcc,omitempty"`
}

type UpdateCardResponse struct {
	CardID      string `json:"card_id"`
	CardOrderID string `json:"card_order_id"`
	Status      string `json:"status"`
}

type BatchCardBalanceResponse struct {
	Data []CardBalanceResponse `json:"data"`
}

type RefillCardParams struct {
	Amount              string         `json:"amount"`
	ClientRequestID     string         `json:"client_request_id,omitempty"`
	X402ReferenceID     string         `json:"x402_reference_id,omitempty"`
	X402Version         int            `json:"x402_version,omitempty"`
	PaymentPayload      map[string]any `json:"payment_payload,omitempty"`
	PaymentRequirements map[string]any `json:"payment_requirements,omitempty"`
	PayerAddress        string         `json:"payer_address,omitempty"`
}

type RefillResponse struct {
	CardID           string `json:"card_id"`
	RefillOrderID    string `json:"refill_order_id"`
	RefilledAmount   string `json:"refilled_amount"`
	Status           string `json:"status"`
	RelatedTransferID string `json:"related_transfer_id,omitempty"`
	X402PaymentID    string `json:"x402_payment_id,omitempty"`
}

// ─── Options ──────────────────────────────────────────────────────────────────

// Options holds the configuration for the Client.
type Options struct {
	APIKey    string
	APISecret string
	BaseURL   string
	// ClientID is optional. If empty, NewClient resolves it via whoami/bootstrap.
	ClientID string
}

// ─── Client ───────────────────────────────────────────────────────────────────

// Client is the Clawallex API client. All methods are safe for concurrent use.
type Client struct {
	opts     Options
	ClientID string
	http     *http.Client
}

// NewClient creates a fully initialised Client.
//   - If opts.ClientID is set it is used directly.
//   - Otherwise calls GET /auth/whoami; if already bound uses the existing
//     bound_client_id, else calls POST /auth/bootstrap to obtain one.
func NewClient(ctx context.Context, opts Options) (*Client, error) {
	c := &Client{
		opts: opts,
		http: &http.Client{Timeout: 30 * time.Second},
	}

	if opts.ClientID != "" {
		c.ClientID = opts.ClientID
		return c, nil
	}

	type whoamiResp struct {
		ClientIDBound bool   `json:"client_id_bound"`
		BoundClientID string `json:"bound_client_id"`
	}
	var whoami whoamiResp
	if err := c.doRequest(ctx, "GET", "/auth/whoami", nil, nil, false, &whoami); err != nil {
		return nil, fmt.Errorf("clawallex: whoami: %w", err)
	}

	if whoami.ClientIDBound {
		c.ClientID = whoami.BoundClientID
		return c, nil
	}

	type bootstrapResp struct {
		ClientID string `json:"client_id"`
	}
	var bootstrap bootstrapResp
	if err := c.doRequest(ctx, "POST", "/auth/bootstrap", nil, struct{}{}, false, &bootstrap); err != nil {
		return nil, fmt.Errorf("clawallex: bootstrap: %w", err)
	}
	c.ClientID = bootstrap.ClientID
	return c, nil
}

// ─── HTTP internals ───────────────────────────────────────────────────────────

const basePath = "/api/v1"

func (c *Client) sign(method, path, rawBody string, includeClientID bool) map[string]string {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	h := sha256.Sum256([]byte(rawBody))
	bodyHash := hex.EncodeToString(h[:])
	canonical := method + "\n" + path + "\n" + ts + "\n" + bodyHash
	mac := hmac.New(sha256.New, []byte(c.opts.APISecret))
	mac.Write([]byte(canonical))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	headers := map[string]string{
		"X-API-Key":    c.opts.APIKey,
		"X-Timestamp":  ts,
		"X-Signature":  sig,
		"Content-Type": "application/json",
	}
	if includeClientID {
		headers["X-Client-Id"] = c.ClientID
	}
	return headers
}

func (c *Client) doRequest(ctx context.Context, method, path string, query url.Values, body any, includeClientID bool, out any) error {
	fullPath := basePath + path
	rawURL := strings.TrimRight(c.opts.BaseURL, "/") + fullPath
	if len(query) > 0 {
		rawURL += "?" + query.Encode()
	}

	var rawBody string
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rawBody = string(b)
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, rawURL, bodyReader)
	if err != nil {
		return err
	}
	for k, v := range c.sign(method, fullPath, rawBody, includeClientID) {
		req.Header.Set(k, v)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode == 402 {
		var envelope struct {
			Code    string              `json:"code"`
			Message string              `json:"message"`
			Details CardOrder402Details `json:"details"`
		}
		_ = json.Unmarshal(respBody, &envelope)
		if envelope.Code == "" {
			envelope.Code = "PAYMENT_REQUIRED"
		}
		return &PaymentRequiredError{Code: envelope.Code, Message: envelope.Message, Details: envelope.Details}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(respBody, &apiErr); err != nil || apiErr.Code == "" {
			return &APIError{StatusCode: resp.StatusCode, Code: "UNKNOWN_ERROR", Message: string(respBody)}
		}
		return &APIError{StatusCode: resp.StatusCode, Code: apiErr.Code, Message: apiErr.Message}
	}

	if out != nil {
		return json.Unmarshal(respBody, out)
	}
	return nil
}

func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	return c.doRequest(ctx, "GET", path, query, nil, true, out)
}

func (c *Client) post(ctx context.Context, path string, body any, out any) error {
	return c.doRequest(ctx, "POST", path, nil, body, true, out)
}

// ─── Wallet ───────────────────────────────────────────────────────────────────

func (c *Client) WalletDetail(ctx context.Context) (*WalletDetail, error) {
	var out WalletDetail
	return &out, c.get(ctx, "/payment/wallets/detail", nil, &out)
}

func (c *Client) RechargeAddresses(ctx context.Context, walletID string) (*RechargeAddressesResponse, error) {
	var out RechargeAddressesResponse
	return &out, c.get(ctx, "/payment/wallets/"+walletID+"/recharge-addresses", nil, &out)
}

// ─── X402 ─────────────────────────────────────────────────────────────────────

func (c *Client) X402PayeeAddress(ctx context.Context, tokenCode string, chainCode ...string) (*PayeeAddressResponse, error) {
	chain := "ETH"
	if len(chainCode) > 0 && chainCode[0] != "" {
		chain = chainCode[0]
	}
	q := url.Values{"chain_code": {chain}, "token_code": {tokenCode}}
	var out PayeeAddressResponse
	return &out, c.get(ctx, "/payment/x402/payee-address", q, &out)
}

func (c *Client) X402AssetAddress(ctx context.Context, tokenCode string, chainCode ...string) (*AssetAddressResponse, error) {
	chain := "ETH"
	if len(chainCode) > 0 && chainCode[0] != "" {
		chain = chainCode[0]
	}
	q := url.Values{"chain_code": {chain}, "token_code": {tokenCode}}
	var out AssetAddressResponse
	return &out, c.get(ctx, "/payment/x402/asset-address", q, &out)
}

// ─── Cards ────────────────────────────────────────────────────────────────────

// NewCard creates a card order.
//
// For Mode B, the first call returns a *PaymentRequiredError. Use
// errors.As to extract it and read Details for the payment challenge,
// then call NewCard again with the same ClientRequestID and the payment
// fields populated.
func (c *Client) NewCard(ctx context.Context, params NewCardParams) (*CardOrderResponse, error) {
	var out CardOrderResponse
	return &out, c.post(ctx, "/payment/card-orders", params, &out)
}

func (c *Client) CardList(ctx context.Context, params CardListParams) (*CardListResponse, error) {
	q := url.Values{}
	if params.Page > 0 {
		q.Set("page", strconv.Itoa(params.Page))
	}
	if params.PageSize > 0 {
		q.Set("page_size", strconv.Itoa(params.PageSize))
	}
	var out CardListResponse
	return &out, c.get(ctx, "/payment/cards", q, &out)
}

func (c *Client) CardBalance(ctx context.Context, cardID string) (*CardBalanceResponse, error) {
	var out CardBalanceResponse
	return &out, c.get(ctx, "/payment/cards/"+cardID+"/balance", nil, &out)
}

func (c *Client) CardDetails(ctx context.Context, cardID string) (*CardDetailsResponse, error) {
	var out CardDetailsResponse
	return &out, c.get(ctx, "/payment/cards/"+cardID+"/details", nil, &out)
}

func (c *Client) BatchCardBalances(ctx context.Context, cardIDs []string) (*BatchCardBalanceResponse, error) {
	type batchBalanceReq struct {
		CardIDs []string `json:"card_ids"`
	}
	var out BatchCardBalanceResponse
	return &out, c.post(ctx, "/payment/cards/balances", batchBalanceReq{CardIDs: cardIDs}, &out)
}

func (c *Client) UpdateCard(ctx context.Context, cardID string, params UpdateCardParams) (*UpdateCardResponse, error) {
	var out UpdateCardResponse
	return &out, c.post(ctx, "/payment/cards/"+cardID+"/update", params, &out)
}

// ─── Transactions ─────────────────────────────────────────────────────────────

func (c *Client) TransactionList(ctx context.Context, params TransactionListParams) (*TransactionListResponse, error) {
	q := url.Values{}
	if params.CardTxID != "" {
		q.Set("card_tx_id", params.CardTxID)
	}
	if params.IssuerTxID != "" {
		q.Set("issuer_tx_id", params.IssuerTxID)
	}
	if params.CardID != "" {
		q.Set("card_id", params.CardID)
	}
	if params.Page > 0 {
		q.Set("page", strconv.Itoa(params.Page))
	}
	if params.PageSize > 0 {
		q.Set("page_size", strconv.Itoa(params.PageSize))
	}
	var out TransactionListResponse
	return &out, c.get(ctx, "/payment/transactions", q, &out)
}

// ─── Refill ───────────────────────────────────────────────────────────────────

func (c *Client) RefillCard(ctx context.Context, cardID string, params RefillCardParams) (*RefillResponse, error) {
	var out RefillResponse
	return &out, c.post(ctx, "/payment/cards/"+cardID+"/refill", params, &out)
}
