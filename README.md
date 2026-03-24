# clawallex-sdk (Go)

Go SDK for the Clawallex Payment API. Requires Go 1.21+. Zero external dependencies.

## Installation

```bash
go get github.com/clawallex/go-sdk
```

## Quick Start

```go
import "github.com/clawallex/go-sdk"

// First run — SDK auto-resolves ClientID via whoami/bootstrap
client, err := clawallex.NewClient(ctx, clawallex.Options{
    APIKey:    "your-api-key",
    APISecret: "your-api-secret",
    BaseURL:   "https://api.clawallex.com",
})

// ⬇️ Persist client.ClientID to your config/database/env
// e.g. "ca_8f0d2c3e5a1b4c7d"
fmt.Println(client.ClientID)

// Subsequent runs — pass the stored ClientID to skip network calls
client, err = clawallex.NewClient(ctx, clawallex.Options{
    APIKey:    "your-api-key",
    APISecret: "your-api-secret",
    BaseURL:   "https://api.clawallex.com",
    ClientID:  "ca_8f0d2c3e5a1b4c7d", // the value you persisted
})
```

## Client ID

`client_id` is your application's stable identity on Clawallex, separate from the API Key.

- You can rotate API Keys (revoke old, create new) without losing access to existing cards and transactions — just keep using the same `client_id`
- When a new API Key sends its first request with an existing `client_id`, the server auto-binds the new key to that identity
- Once bound, a `client_id` cannot be changed for that API Key (TOFU — Trust On First Use)
- Cards and transactions are isolated by `client_id` — different `client_id`s cannot see each other's data
- Wallet balance is shared at the user level (across all `client_id`s under the same user)

### Resolution

If `client_id` is provided at initialization, the SDK uses it directly (no network calls). If omitted, the SDK calls `GET /auth/whoami` — if already bound, uses the existing `client_id`; if not, calls `POST /auth/bootstrap` to generate and bind a new one.

### Best Practice

Persist the resolved `client_id` after the first initialization and pass it explicitly on subsequent sessions. This avoids unnecessary network calls and ensures identity continuity across API Key rotations.

### Data Isolation

| Scope | Isolation Level |
|-------|----------------|
| Wallet balance | User-level — shared across all `client_id`s under the same user |
| Cards | `client_id`-scoped — only visible to the `client_id` that created them |
| Transactions | `client_id`-scoped — only visible to the `client_id` that owns the card |
| Recharge addresses | User-level — shared |

## API

```go
// Wallet
client.WalletDetail(ctx)
client.RechargeAddresses(ctx, walletID)

// X402 — chainCode is optional, defaults to "ETH"
client.X402PayeeAddress(ctx, "USDC")           // ETH chain
client.X402AssetAddress(ctx, "USDC", "BASE")   // explicit chain

// Cards
client.NewCard(ctx, params)
client.CardList(ctx, params)
client.CardBalance(ctx, cardID)
client.CardDetails(ctx, cardID)

// Transactions
client.TransactionList(ctx, params)

// Refill
client.RefillCard(ctx, cardID, params)
```

## Mode A — Wallet Funded Card

Mode A is the simplest path: cards are paid from your Clawallex wallet balance. No blockchain interaction needed.

### Create a Card

```go
import "github.com/google/uuid"

order, err := client.NewCard(ctx, clawallex.NewCardParams{
    ModeCode:        100,               // Mode A
    CardType:        100,               // 100=flash, 200=stream
    Amount:          "50.0000",         // card face value in USD
    ClientRequestID: uuid.NewString(),  // idempotency key
})

// order.CardOrderID — always present
// order.CardID      — present if card created synchronously
// order.Status      — 200=active, 120=pending_async (issuer processing)
```

### Handling Async Card Creation (status=120)

Card creation may be asynchronous — the issuer accepts the request but hasn't finished yet. **This is normal**, not an error. The wallet has already been charged.

```go
if order.Status == 120 || order.CardID == "" {
    // Poll card list until the new card appears
    before, _ := client.CardList(ctx, clawallex.CardListParams{Page: 1, PageSize: 100})
    existing := map[string]bool{}
    for _, c := range before.Data {
        existing[c.CardID] = true
    }

    var cardID string
    for i := 0; i < 30; i++ {
        time.Sleep(2 * time.Second)
        list, _ := client.CardList(ctx, clawallex.CardListParams{Page: 1, PageSize: 100})
        for _, c := range list.Data {
            if !existing[c.CardID] {
                cardID = c.CardID
                break
            }
        }
        if cardID != "" {
            break
        }
    }
}
```

> **Tip**: You can also retry `NewCard` with the same `ClientRequestID`. The server will safely retry the issuer call without re-charging your wallet.

### Mode A Refill

```go
refill, err := client.RefillCard(ctx, cardID, clawallex.RefillCardParams{
    Amount:          "30.0000",
    ClientRequestID: uuid.NewString(),  // idempotency key for Mode A
})
```

## Fee Structure

Fees are calculated server-side. For Mode B, the 402 response breaks them down:

| Fee field | Applies to | Description |
|-----------|-----------|-------------|
| `issue_fee_amount` | All cards | One-time card issuance fee |
| `monthly_fee_amount` | Stream cards only | First month fee (included in initial charge) |
| `fx_fee_amount` | All cards | Foreign exchange fee |
| `fee_amount` | — | `= issue_fee_amount + monthly_fee_amount + fx_fee_amount` |
| `payable_amount` | — | `= amount + fee_amount` (total to pay) |

- Flash cards: `fee_amount = issue_fee + fx_fee`
- Stream cards: `fee_amount = issue_fee + monthly_fee + fx_fee`
- Mode A refill: **no fees** — the refill amount goes directly to the card
- Mode B refill: **no fees** — same as Mode A

## Mode B — x402 On-Chain Payment (Two-Step)

Mode B is for Agents that hold their own wallet and private key. The card is funded by an on-chain USDC transfer via the EIP-3009 `transferWithAuthorization` standard — no human intervention needed.

> **Mode B currently only supports USDC** (6 decimals) on ETH and BASE chains. `token_code` must be `"USDC"`.

### Flow

```
Agent → POST /card-orders (mode_code=200)     → 402 + quote details
Agent → sign EIP-3009 with private key
Agent → POST /card-orders (same client_request_id) → 200 + card created
```

### Stage 1 — Request Quote (402 is expected, not an error)

```go
import (
    "errors"

    "github.com/google/uuid"
    "github.com/clawallex/go-sdk"
)

clientRequestID := uuid.NewString()
var details *clawallex.CardOrder402Details

_, err := client.NewCard(ctx, clawallex.NewCardParams{
    ModeCode:        200,
    CardType:        200,           // 100=flash, 200=stream
    Amount:          "200.0000",
    ClientRequestID: clientRequestID,
    ChainCode:       "ETH",        // or "BASE"
    TokenCode:       "USDC",
})
var payErr *clawallex.PaymentRequiredError
if errors.As(err, &payErr) {
    details = payErr.Details
    // details.PayeeAddress    — system receiving address
    // details.AssetAddress    — USDC contract address
    // details.PayableAmount   — total including fees (e.g. "207.5900")
    // details.X402ReferenceID — must be echoed in Stage 2
    // details.FinalCardAmount, details.FeeAmount,
    // details.IssueFeeAmount, details.MonthlyFeeAmount, details.FxFeeAmount
}
```

### EIP-3009 Signing (using go-ethereum)

```go
import (
    "crypto/ecdsa"
    "crypto/rand"
    "math"
    "math/big"
    "strconv"
    "time"

    "github.com/ethereum/go-ethereum/common"
    "github.com/ethereum/go-ethereum/crypto"
    "github.com/ethereum/go-ethereum/signer/core/apitypes"
)

privateKey, _ := crypto.HexToECDSA(PRIVATE_KEY_HEX)
fromAddress := crypto.PubkeyToAddress(privateKey.PublicKey)

payableFloat, _ := strconv.ParseFloat(details.PayableAmount, 64)
maxAmountRequired := strconv.FormatInt(int64(math.Floor(payableFloat*1_000_000)), 10)
now := time.Now().Unix()

var nonceBytes [32]byte
rand.Read(nonceBytes[:])
nonce := common.Bytes2Hex(nonceBytes[:])

// Build EIP-712 typed data using go-ethereum's apitypes package.
// Domain name is "USDC" on Sepolia; may be "USD Coin" on mainnet.
// Sign using crypto.Sign after hashing the EIP-712 struct.
// chainId: 11155111 (Sepolia), 1 (ETH mainnet), 8453 (BASE)
typedData := apitypes.TypedData{
    Types: apitypes.Types{
        "EIP712Domain": {
            {Name: "name", Type: "string"},
            {Name: "version", Type: "string"},
            {Name: "chainId", Type: "uint256"},
            {Name: "verifyingContract", Type: "address"},
        },
        "TransferWithAuthorization": {
            {Name: "from", Type: "address"},
            {Name: "to", Type: "address"},
            {Name: "value", Type: "uint256"},
            {Name: "validAfter", Type: "uint256"},
            {Name: "validBefore", Type: "uint256"},
            {Name: "nonce", Type: "bytes32"},
        },
    },
    PrimaryType: "TransferWithAuthorization",
    Domain: apitypes.TypedDataDomain{
        Name:              "USDC",
        Version:           "2",
        ChainId:           math.NewHexOrDecimal256(11155111),
        VerifyingContract: details.AssetAddress,
    },
    Message: apitypes.TypedDataMessage{
        "from":        fromAddress.Hex(),
        "to":          details.PayeeAddress,
        "value":       maxAmountRequired,
        "validAfter":  strconv.FormatInt(now-60, 10),
        "validBefore": strconv.FormatInt(now+3600, 10),
        "nonce":       "0x" + nonce,
    },
}

domainSep, _ := typedData.HashStruct("EIP712Domain", typedData.Domain.Map())
messageSep, _ := typedData.HashStruct(typedData.PrimaryType, typedData.Message)
rawData := append([]byte{0x19, 0x01}, append(domainSep, messageSep...)...)
hash := crypto.Keccak256(rawData)
sig, _ := crypto.Sign(hash, privateKey)
sig[64] += 27 // adjust v
signature := common.Bytes2Hex(sig)
```

> **Note**: The EIP-712 domain `name` depends on the USDC contract deployment.
> On Sepolia testnet it is `"USDC"`, on mainnet it may be `"USD Coin"`.
> Query the contract's `name()` method to confirm.

### Stage 2 — Submit Payment

> **IMPORTANT**: Stage 2 **must** use the same `ClientRequestID` as Stage 1.
> A different `ClientRequestID` will create a **new** card order instead of completing the current one.

The SDK provides typed structs `X402Authorization`, `X402PaymentPayload`, and `X402PaymentRequirements` with a `.ToMap()` method for building the request:

```go
authorization := clawallex.X402Authorization{
    From:        fromAddress.Hex(),
    To:          details.PayeeAddress,
    Value:       maxAmountRequired,
    ValidAfter:  strconv.FormatInt(now-60, 10),
    ValidBefore: strconv.FormatInt(now+3600, 10),
    Nonce:       "0x" + nonce,
}

payload := clawallex.X402PaymentPayload{
    Scheme:  "exact",
    Network: "ETH",
}
payload.Payload.Signature = signature
payload.Payload.Authorization = authorization

requirements := clawallex.X402PaymentRequirements{
    Scheme:            "exact",
    Network:           "ETH",                       // must equal payload.Network
    Asset:             details.AssetAddress,         // must equal 402 AssetAddress
    PayTo:             details.PayeeAddress,         // must equal authorization.To
    MaxAmountRequired: maxAmountRequired,            // must equal authorization.Value
}
requirements.Extra.ReferenceID = details.X402ReferenceID

order, err := client.NewCard(ctx, clawallex.NewCardParams{
    ModeCode:            200,
    CardType:            200,
    Amount:              "200.0000",
    ClientRequestID:     clientRequestID,             // MUST reuse from Stage 1
    X402Version:         1,
    PaymentPayload:      payload.ToMap(),
    PaymentRequirements: requirements.ToMap(),
    Extra:               map[string]string{
        "card_amount": details.FinalCardAmount,
        "paid_amount": details.PayableAmount,
    },
    PayerAddress:        fromAddress.Hex(),
})
// order.CardOrderID, order.CardID, order.Status
```

### Mode B Refill (No 402 — Direct Submit)

Refill has **no 402 challenge**. Query addresses first, then submit directly:

```go
// 1. query addresses
payee, _ := client.X402PayeeAddress(ctx, "USDC")       // defaults to ETH
asset, _ := client.X402AssetAddress(ctx, "USDC", "ETH")

// 2. sign EIP-3009 (same as above, but amount has no fee)
refillAmount := "30.0000"
maxAmt := strconv.FormatInt(int64(math.Floor(30.0000*1_000_000)), 10)
// ... sign with privateKey ...

// 3. submit refill
refill, err := client.RefillCard(ctx, cardID, clawallex.RefillCardParams{
    Amount:              refillAmount,
    X402ReferenceID:     uuid.NewString(),            // unique per refill
    X402Version:         1,
    PaymentPayload:      payload.ToMap(),
    PaymentRequirements: requirements.ToMap(),
    PayerAddress:        fromAddress.Hex(),
})
```

### Consistency Rules (Server Rejects if Any Fail)

| # | Rule |
|---|------|
| 1 | `payment_payload.network` == `payment_requirements.network` |
| 2 | `authorization.to` == `payTo` == 402 `payee_address` |
| 3 | `authorization.value` == `maxAmountRequired` == `payable_amount × 10^6` |
| 4 | `payment_requirements.asset` == 402 `asset_address` |
| 5 | `extra.referenceId` == 402 `x402_reference_id` |
| 6 | `extra.card_amount` == original `amount` |
| 7 | `extra.paid_amount` == 402 `payable_amount` |

## Card Details — Decrypting PAN/CVV

`CardDetails` returns encrypted sensitive data. The server encrypts with a key derived from your `api_secret`.

```go
import (
    "crypto/aes"
    "crypto/cipher"
    "encoding/base64"
    "encoding/json"

    "golang.org/x/crypto/hkdf"
)

details, _ := client.CardDetails(ctx, cardID)
enc := details.EncryptedSensitiveData
// enc.Version = "v1", enc.Algorithm = "AES-256-GCM", enc.KDF = "HKDF-SHA256"

// 1. Derive 32-byte key from apiSecret using HKDF-SHA256
hkdfReader := hkdf.New(sha256.New, []byte(apiSecret), nil, []byte("clawallex-card-sensitive-data"))
derivedKey := make([]byte, 32)
io.ReadFull(hkdfReader, derivedKey)

// 2. Decrypt with AES-256-GCM
nonce, _ := base64.StdEncoding.DecodeString(enc.Nonce)
ciphertext, _ := base64.StdEncoding.DecodeString(enc.Ciphertext)

block, _ := aes.NewCipher(derivedKey)
gcm, _ := cipher.NewGCM(block)
plaintext, _ := gcm.Open(nil, nonce, ciphertext, nil)

var cardData struct {
    PAN string `json:"pan"`
    CVV string `json:"cvv"`
}
json.Unmarshal(plaintext, &cardData)
// cardData.PAN = "4111111111111111", cardData.CVV = "123"
```

> **Security**: Never log or persist the decrypted PAN/CVV in plaintext. The `api_secret` must be at least 16 bytes. Add `golang.org/x/crypto` dependency: `go get golang.org/x/crypto`.

## Error Handling

```go
import "errors"

_, err := client.NewCard(ctx, params)
var pErr *clawallex.PaymentRequiredError
if errors.As(err, &pErr) {
    // Mode B challenge — normal flow
    fmt.Println(pErr.Details.PayeeAddress)
}
var apiErr *clawallex.APIError
if errors.As(err, &apiErr) {
    fmt.Println(apiErr.StatusCode, apiErr.Code, apiErr.Message)
}
```

## Enums Reference

| Constant | Value | Description |
|----------|-------|-------------|
| `mode_code` | `100` | Mode A — wallet funded |
| `mode_code` | `200` | Mode B — x402 on-chain |
| `card_type` | `100` | Flash card |
| `card_type` | `200` | Stream card (subscription) |
| `card.status` | `200` | Active |
| `card.status` | `220` | Closing |
| `card.status` | `230` | Expired |
| `card.status` | `250` | Cancelled |
| `wallet.status` | `100` | Normal |
| `wallet.status` | `210` | Frozen |
