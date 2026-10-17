package store

import "github.com/uptrace/bun"

type AssistanceCase struct {
	bun.BaseModel `bun:"table:assistance_cases,alias:ac"`
	ID            int64  `bun:"id,pk,autoincrement" json:"id"`
	DedupeKey     string `json:"-"`
	OrderID       int64  `json:"order_id"`
	TransactionID int64  `json:"transaction_id"`
	AnomalyID     int64  `json:"anomaly_id"`
	Source        string `json:"source"`
	Kind          string `json:"kind"`
	PaymentRef    string `json:"payment_ref"`
	ClaimAmount   int64  `json:"claim_amount"`
	PaidAt        int64  `json:"paid_at"`
	Message       string `json:"message"`
	Contact       string `json:"contact"`
	TokenHash     string `json:"-"`
	Status        string `json:"status"`
	Summary       string `json:"summary"`
	PublicReply   string `json:"public_reply"`
	Attempts      int    `json:"attempts"`
	NextAt        int64  `json:"next_at"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
}

type AssistanceAction struct {
	bun.BaseModel `bun:"table:assistance_actions"`
	ID            int64  `bun:"id,pk,autoincrement" json:"id"`
	CaseID        int64  `json:"case_id"`
	Action        string `json:"action"`
	Reason        string `json:"reason"`
	Result        string `json:"result"`
	CreatedAt     int64  `json:"created_at"`
}
