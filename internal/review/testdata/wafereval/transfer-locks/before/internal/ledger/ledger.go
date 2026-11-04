package ledger

import (
	"errors"
	"sync"
)

var (
	ErrInvalidAmount     = errors.New("ledger: amount must be positive")
	ErrInsufficientFunds = errors.New("ledger: insufficient funds")
)

// Account is a balance in cents.
type Account struct {
	ID      string
	Balance int64
}

// Ledger moves money between accounts.
type Ledger struct {
	mu sync.Mutex
}

// Transfer moves amount from one account to another, atomically.
func (l *Ledger) Transfer(from, to *Account, amount int64) error {
	if amount <= 0 {
		return ErrInvalidAmount
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if from.Balance < amount {
		return ErrInsufficientFunds
	}
	from.Balance -= amount
	to.Balance += amount
	return nil
}
