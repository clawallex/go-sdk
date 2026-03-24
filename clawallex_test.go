package clawallex

import (
	"context"
	"errors"
	"fmt"
	"crypto/rand"
	"os"
	"testing"
	"time"
)

func skipIfNoEnv(t *testing.T) {
	t.Helper()
	if os.Getenv("CLAWALLEX_API_KEY") == "" ||
		os.Getenv("CLAWALLEX_API_SECRET") == "" ||
		os.Getenv("CLAWALLEX_BASE_URL") == "" {
		t.Skip("Set CLAWALLEX_API_KEY, CLAWALLEX_API_SECRET, CLAWALLEX_BASE_URL to run")
	}
}

func newTestClient(t *testing.T) *Client {
	t.Helper()
	skipIfNoEnv(t)
	ctx := context.Background()
	c, err := NewClient(ctx, Options{
		APIKey:    os.Getenv("CLAWALLEX_API_KEY"),
		APISecret: os.Getenv("CLAWALLEX_API_SECRET"),
		BaseURL:   os.Getenv("CLAWALLEX_BASE_URL"),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

// ── Auth ────────────────────────────────────────────────────────────────────

func TestClientID(t *testing.T) {
	c := newTestClient(t)
	if c.ClientID == "" {
		t.Fatal("expected non-empty ClientID")
	}
}

func TestSecondCreateReusesClientID(t *testing.T) {
	c1 := newTestClient(t)
	ctx := context.Background()
	c2, err := NewClient(ctx, Options{
		APIKey:    os.Getenv("CLAWALLEX_API_KEY"),
		APISecret: os.Getenv("CLAWALLEX_API_SECRET"),
		BaseURL:   os.Getenv("CLAWALLEX_BASE_URL"),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c1.ClientID != c2.ClientID {
		t.Fatalf("expected same ClientID, got %q vs %q", c1.ClientID, c2.ClientID)
	}
}

func TestExplicitClientID(t *testing.T) {
	c1 := newTestClient(t)
	ctx := context.Background()
	c2, err := NewClient(ctx, Options{
		APIKey:    os.Getenv("CLAWALLEX_API_KEY"),
		APISecret: os.Getenv("CLAWALLEX_API_SECRET"),
		BaseURL:   os.Getenv("CLAWALLEX_BASE_URL"),
		ClientID:  c1.ClientID,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c2.ClientID != c1.ClientID {
		t.Fatalf("expected %q, got %q", c1.ClientID, c2.ClientID)
	}
}

// ── Wallet ──────────────────────────────────────────────────────────────────

func TestWalletDetail(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	w, err := c.WalletDetail(ctx)
	if err != nil {
		t.Fatalf("WalletDetail: %v", err)
	}
	if w.WalletID == "" {
		t.Fatal("expected non-empty wallet_id")
	}
	if w.Currency == "" {
		t.Fatal("expected non-empty currency")
	}
	if w.UpdatedAt == "" {
		t.Fatal("expected non-empty updated_at")
	}
}

func TestRechargeAddresses(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	w, err := c.WalletDetail(ctx)
	if err != nil {
		t.Fatalf("WalletDetail: %v", err)
	}
	result, err := c.RechargeAddresses(ctx, w.WalletID)
	if err != nil {
		t.Fatalf("RechargeAddresses: %v", err)
	}
	if result.WalletID != w.WalletID {
		t.Fatalf("expected wallet_id %q, got %q", w.WalletID, result.WalletID)
	}
	if result.Data == nil {
		t.Fatal("expected non-nil data")
	}
	if len(result.Data) > 0 {
		addr := result.Data[0]
		if addr.ChainCode == "" || addr.TokenCode == "" || addr.Address == "" {
			t.Fatal("expected non-empty address fields")
		}
	}
}

// ── X402 ────────────────────────────────────────────────────────────────────

func TestX402PayeeAddressDefaultChain(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	result, err := c.X402PayeeAddress(ctx, "USDC")
	if err != nil {
		t.Fatalf("X402PayeeAddress: %v", err)
	}
	if result.Address == "" {
		t.Fatal("expected non-empty address")
	}
	if result.TokenCode != "USDC" {
		t.Fatalf("expected token_code USDC, got %q", result.TokenCode)
	}
}

func TestX402PayeeAddressExplicitChain(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	result, err := c.X402PayeeAddress(ctx, "USDC", "ETH")
	if err != nil {
		t.Fatalf("X402PayeeAddress: %v", err)
	}
	if result.Address == "" {
		t.Fatal("expected non-empty address")
	}
	if result.ChainCode != "ETH" {
		t.Fatalf("expected chain_code ETH, got %q", result.ChainCode)
	}
}

func TestX402AssetAddressDefaultChain(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	result, err := c.X402AssetAddress(ctx, "USDC")
	if err != nil {
		t.Fatalf("X402AssetAddress: %v", err)
	}
	if result.AssetAddress == "" {
		t.Fatal("expected non-empty asset_address")
	}
	if result.TokenCode != "USDC" {
		t.Fatalf("expected token_code USDC, got %q", result.TokenCode)
	}
}

func TestX402AssetAddressExplicitChain(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	result, err := c.X402AssetAddress(ctx, "USDC", "ETH")
	if err != nil {
		t.Fatalf("X402AssetAddress: %v", err)
	}
	if result.AssetAddress == "" {
		t.Fatal("expected non-empty asset_address")
	}
	if result.ChainCode != "ETH" {
		t.Fatalf("expected chain_code ETH, got %q", result.ChainCode)
	}
}

// ── Cards ───────────────────────────────────────────────────────────────────

func TestCardListPagination(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	result, err := c.CardList(ctx, CardListParams{Page: 1, PageSize: 5})
	if err != nil {
		t.Fatalf("CardList: %v", err)
	}
	if result.Page != 1 || result.PageSize != 5 {
		t.Fatalf("expected page=1 page_size=5, got page=%d page_size=%d", result.Page, result.PageSize)
	}
	if result.Data == nil {
		t.Fatal("expected non-nil data")
	}
}

func TestCardListDefaults(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	result, err := c.CardList(ctx, CardListParams{})
	if err != nil {
		t.Fatalf("CardList: %v", err)
	}
	if result.Data == nil {
		t.Fatal("expected non-nil data")
	}
}

func TestCardBalance(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	cards, err := c.CardList(ctx, CardListParams{Page: 1, PageSize: 1})
	if err != nil {
		t.Fatalf("CardList: %v", err)
	}
	if len(cards.Data) == 0 {
		t.Skip("no cards")
	}
	card := cards.Data[0]
	balance, err := c.CardBalance(ctx, card.CardID)
	if err != nil {
		t.Fatalf("CardBalance: %v", err)
	}
	if balance.CardID != card.CardID {
		t.Fatalf("expected card_id %q, got %q", card.CardID, balance.CardID)
	}
	if balance.CardCurrency == "" {
		t.Fatal("expected non-empty card_currency")
	}
}

func TestCardDetails(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	cards, err := c.CardList(ctx, CardListParams{Page: 1, PageSize: 1})
	if err != nil {
		t.Fatalf("CardList: %v", err)
	}
	if len(cards.Data) == 0 {
		t.Skip("no cards")
	}
	card := cards.Data[0]
	details, err := c.CardDetails(ctx, card.CardID)
	if err != nil {
		t.Fatalf("CardDetails: %v", err)
	}
	if details.CardID != card.CardID {
		t.Fatalf("expected card_id %q, got %q", card.CardID, details.CardID)
	}
	if details.MaskedPAN == "" {
		t.Fatal("expected non-empty masked_pan")
	}
	if details.EncryptedSensitiveData.Version != "v1" {
		t.Fatalf("expected version v1, got %q", details.EncryptedSensitiveData.Version)
	}
	if details.EncryptedSensitiveData.Algorithm != "AES-256-GCM" {
		t.Fatalf("expected algorithm AES-256-GCM, got %q", details.EncryptedSensitiveData.Algorithm)
	}
	if details.EncryptedSensitiveData.Ciphertext == "" {
		t.Fatal("expected non-empty ciphertext")
	}
}

func TestCardBalanceNotFound(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	_, err := c.CardBalance(ctx, "non_existent_card_id")
	if err == nil {
		t.Fatal("expected error for non-existent card")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode < 400 {
		t.Fatalf("expected status >= 400, got %d", apiErr.StatusCode)
	}
}

// ── Transactions ────────────────────────────────────────────────────────────

func TestTransactionListPagination(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	result, err := c.TransactionList(ctx, TransactionListParams{Page: 1, PageSize: 5})
	if err != nil {
		t.Fatalf("TransactionList: %v", err)
	}
	if result.Data == nil {
		t.Fatal("expected non-nil data")
	}
}

func TestTransactionListDefaults(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	result, err := c.TransactionList(ctx, TransactionListParams{})
	if err != nil {
		t.Fatalf("TransactionList: %v", err)
	}
	if result.Data == nil {
		t.Fatal("expected non-nil data")
	}
}

func TestTransactionListFields(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	result, err := c.TransactionList(ctx, TransactionListParams{Page: 1, PageSize: 5})
	if err != nil {
		t.Fatalf("TransactionList: %v", err)
	}
	if len(result.Data) == 0 {
		t.Skip("no transactions")
	}
	tx := result.Data[0]
	if tx.CardID == "" {
		t.Fatal("expected non-empty card_id")
	}
	if tx.CardTxID == "" {
		t.Fatal("expected non-empty card_tx_id")
	}
}

func TestTransactionFilterByCard(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	cards, err := c.CardList(ctx, CardListParams{Page: 1, PageSize: 1})
	if err != nil {
		t.Fatalf("CardList: %v", err)
	}
	if len(cards.Data) == 0 {
		t.Skip("no cards")
	}
	cardID := cards.Data[0].CardID
	result, err := c.TransactionList(ctx, TransactionListParams{CardID: cardID, Page: 1, PageSize: 5})
	if err != nil {
		t.Fatalf("TransactionList: %v", err)
	}
	for _, tx := range result.Data {
		if tx.CardID != cardID {
			t.Fatalf("expected card_id %q, got %q", cardID, tx.CardID)
		}
	}
}

// ── Mode A card lifecycle ───────────────────────────────────────────────────

func TestModeACreateVerifyClose(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	// 1. create flash card
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	reqID := fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
	order, err := c.NewCard(ctx, NewCardParams{
		ModeCode:        ModeWallet,
		CardType:        Flash,
		Amount:          "5.0000",
		ClientRequestID: reqID,
	})
	if err != nil {
		t.Fatalf("NewCard: %v", err)
	}
	if order.CardOrderID == "" {
		t.Fatal("expected non-empty card_order_id")
	}

	// snapshot existing card ids before polling
	existingCards := map[string]bool{}
	if before, err := c.CardList(ctx, CardListParams{Page: 1, PageSize: 100}); err == nil {
		for _, card := range before.Data {
			existingCards[card.CardID] = true
		}
	}

	// card creation may be async (status=120), poll card list for new card
	var cardID string
	if order.CardID != "" {
		cardID = order.CardID
	} else {
		t.Logf("card order %s is async (status=%d), polling card list...", order.CardOrderID, order.Status)
		for i := 0; i < 30; i++ {
			time.Sleep(2 * time.Second)
			list, listErr := c.CardList(ctx, CardListParams{Page: 1, PageSize: 100})
			if listErr != nil {
				t.Logf("poll %d: CardList err=%v", i+1, listErr)
				continue
			}
			for _, card := range list.Data {
				if !existingCards[card.CardID] && card.ModeCode == ModeWallet {
					cardID = card.CardID
					t.Logf("poll %d: found new card %s", i+1, cardID)
					break
				}
			}
			if cardID != "" {
				break
			}
		}
		if cardID == "" {
			t.Fatal("new card not found after 60s polling")
		}
	}

	// 3. check balance
	balance, err := c.CardBalance(ctx, cardID)
	if err != nil {
		t.Fatalf("CardBalance: %v", err)
	}
	if balance.CardID != cardID {
		t.Fatalf("expected card_id %q, got %q", cardID, balance.CardID)
	}

	// 4. check details
	details, err := c.CardDetails(ctx, cardID)
	if err != nil {
		t.Fatalf("CardDetails: %v", err)
	}
	if details.CardID != cardID {
		t.Fatalf("expected card_id %q, got %q", cardID, details.CardID)
	}
	if details.EncryptedSensitiveData.Ciphertext == "" {
		t.Fatal("expected non-empty ciphertext")
	}

}

// ── Mode B 402 flow ─────────────────────────────────────────────────────────

func TestModeBReturns402(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	clientReqID := fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])

	const payerAddr = "0x850E5F8D352CC8f501754f8835eE28e4ea4Ba68C"
	const dummyAddr = "0x0000000000000000000000000000000000000000"
	const dummyNonce = "0x0000000000000000000000000000000000000000000000000000000000000000"
	const dummySig = "0x000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"

	_, err := c.NewCard(ctx, NewCardParams{
		ModeCode:        ModeX402,
		CardType:        Stream,
		Amount:          "1.0000",
		ClientRequestID: clientReqID,
		ChainCode:       "ETH",
		TokenCode:       "USDC",
		PayerAddress:    payerAddr,
		X402Version:     1,
		PaymentPayload: map[string]any{
			"scheme": "exact", "network": "ETH",
			"payload": map[string]any{
				"signature": dummySig,
				"authorization": map[string]any{
					"from": payerAddr, "to": dummyAddr,
					"value": "1050000", "validAfter": "0", "validBefore": "9999999999",
					"nonce": dummyNonce,
				},
			},
		},
		PaymentRequirements: map[string]any{
			"scheme": "exact", "network": "ETH",
			"asset": dummyAddr, "payTo": dummyAddr,
			"maxAmountRequired": "1050000",
			"extra": map[string]any{"referenceId": "dummy"},
		},
		Extra: map[string]string{"card_amount": "1.0000", "paid_amount": "1.0500"},
	})
	if err == nil {
		t.Fatal("expected 402 PaymentRequiredError")
	}
	var payErr *PaymentRequiredError
	if !errors.As(err, &payErr) {
		t.Fatalf("expected PaymentRequiredError, got %T: %v", err, err)
	}
	if payErr.Code != "PAYMENT_REQUIRED" {
		t.Fatalf("expected code PAYMENT_REQUIRED, got %q", payErr.Code)
	}
	if payErr.Details.CardOrderID == "" {
		t.Fatal("expected non-empty card_order_id")
	}
	if payErr.Details.X402ReferenceID == "" {
		t.Fatal("expected non-empty x402_reference_id")
	}
	if payErr.Details.PayeeAddress == "" {
		t.Fatal("expected non-empty payee_address")
	}
	if payErr.Details.AssetAddress == "" {
		t.Fatal("expected non-empty asset_address")
	}
	if payErr.Details.PayableAmount == "" {
		t.Fatal("expected non-empty payable_amount")
	}
	if payErr.Details.FeeAmount == "" {
		t.Fatal("expected non-empty fee_amount")
	}
}
