package logger

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/vm"
)

type diff[T any] struct {
	before T
	after  T
}

type accountDiff struct {
	balance diff[*big.Int]
	storage map[common.Hash]diff[common.Hash]
	code    diff[[]byte]
}

type accountsDiff map[common.Address]accountDiff

func (a *accountsDiff) tryInitAddr(addr common.Address) {
	accsDiff := *a
	if _, ok := accsDiff[addr]; !ok {
		accsDiff[addr] = accountDiff{
			balance: diff[*big.Int]{nil, nil},
			storage: make(map[common.Hash]diff[common.Hash]),
			code:    diff[[]byte]{nil, nil},
		}
	}
}

// update balance
func (a *accountsDiff) recordBalance(acc common.Address, before, after *big.Int) {
	a.tryInitAddr(acc)
	accsDiff := *a
	diff := accsDiff[acc]
	if diff.balance.before == nil {
		// take only the initial balance
		diff.balance.before = big.NewInt(0).Set(before)
	}
	diff.balance.after = big.NewInt(0).Set(after)
	accsDiff[acc] = diff
}

type StateDiffLogger struct {
	accounts accountsDiff
}

// transferFunc
func (l *StateDiffLogger) WrappedTransferFunc(db vm.StateDB, sender, recipient common.Address, amount *big.Int) {
	senderBalanceBefore := db.GetBalance(sender)
	recipientBalanceBefore := db.GetBalance(recipient)
	defer func() {
		senderBalanceAfter := db.GetBalance(sender)
		recipientBalanceAfter := db.GetBalance(recipient)
		l.accounts.recordBalance(sender, senderBalanceBefore, senderBalanceAfter)
		l.accounts.recordBalance(recipient, recipientBalanceBefore, recipientBalanceAfter)
	}()
	core.Transfer(db, sender, recipient, amount)
}
