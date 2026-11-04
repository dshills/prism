package ledger

import (
	"errors"
	"sync"
)

var (
	ErrInvalidAmount     = errors.New("ledger: amount must be positive")
	ErrInsufficientFunds = errors.New("ledger: insufficient funds")
)

// Account is a balance in cents. Its mutex guards Balance, so transfers
// between unrelated accounts no longer wait on each other.
type Account struct {
	mu      sync.Mutex
	ID      string
	Balance int64
}

// Ledger moves money between accounts.
type Ledger struct{}

// Transfer moves amount from one account to another, atomically.
func (l *Ledger) Transfer(from, to *Account, amount int64) error {
	if amount <= 0 {
		return ErrInvalidAmount
	}
	from.mu.Lock()
	defer from.mu.Unlock()
	to.mu.Lock()
	defer to.mu.Unlock()
	if from.Balance < amount {
		return ErrInsufficientFunds
	}
	from.Balance -= amount
	to.Balance += amount
	return nil
}

// Current returns the account's balance.
func (a *Account) Current() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Balance
}
