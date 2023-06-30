package native

import (
	"encoding/json"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/eth/tracers"
)

func init() {
	tracers.DefaultDirectory.Register("stateDiffTracer", newStateTracer, false)
}

type diff[T any] struct {
	before T
	after  T
}

type accountDiff struct {
	balanceDelta *big.Int
	nonceDelta   int
	storage      map[common.Hash]diff[common.Hash]
	code         diff[[]byte]
}

// StateDiffLogger implements Tracer interface
type StateDiffTracer struct {
	accounts map[common.Address]accountDiff
	env      *vm.EVM
	gasLimit uint64
	tracer   *callTracer
}

func newStateTracer(ctx *tracers.Context, cfg json.RawMessage) (tracers.Tracer, error) {
	t, err := newCallTracer(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &StateDiffTracer{
		tracer:   t.(*callTracer),
		accounts: make(map[common.Address]accountDiff),
	}, nil
}

// transferFunc
func (l *StateDiffTracer) WrappedTransferFunc(db vm.StateDB, sender, recipient common.Address, amount *big.Int) {
	defer func() {
		// record the balance change
		l.recordBalanceChange(sender, big.NewInt(0).Neg(amount))
		l.recordBalanceChange(recipient, amount)
	}()
	core.Transfer(db, sender, recipient, amount)
}

func (l *StateDiffTracer) CaptureTxStart(gasLimit uint64) {
	l.gasLimit = gasLimit
	l.tracer.CaptureTxStart(gasLimit)
}

func (l *StateDiffTracer) CaptureTxEnd(restGas uint64) {
	l.tracer.CaptureTxEnd(restGas)
	caller := l.tracer.callstack[0].From
	used := l.tracer.callstack[0].GasUsed
	l.recordBalanceChange(caller, big.NewInt(-int64(used)))
}

func (l *StateDiffTracer) CaptureStart(env *vm.EVM, from common.Address, to common.Address, create bool, input []byte, gas uint64, value *big.Int) {
	l.env = env
	l.tracer.CaptureStart(env, from, to, create, input, gas, value)
	if create {
		// record noce increment
		l.recordNonceIncrese(from)
	}
}

func (l *StateDiffTracer) CaptureEnd(output []byte, gasUsed uint64, err error) {
	l.tracer.CaptureEnd(output, gasUsed, err)
	callframe := l.tracer.callstack[0]
	// record gas used
	l.recordBalanceChange(callframe.From, big.NewInt(0).Neg(big.NewInt(int64(gasUsed))))

	if callframe.Type == vm.CREATE || callframe.Type == vm.CREATE2 {
		// record the code
		contract := *callframe.To
		l.recordCode(contract, l.env.StateDB.GetCode(contract))
	}
}

func (l *StateDiffTracer) CaptureEnter(typ vm.OpCode, from common.Address, to common.Address, input []byte, gas uint64, value *big.Int) {
	l.tracer.CaptureEnter(typ, from, to, input, gas, value)
	if typ == vm.CREATE || typ == vm.CREATE2 {
		// record noce increment
		l.recordNonceIncrese(from)
	}
}

func (l *StateDiffTracer) CaptureExit(output []byte, gasUsed uint64, err error) {
	l.tracer.CaptureExit(output, gasUsed, err)
	// retrieve the last callframe in last callstack
	lastCallStack := l.tracer.callstack[len(l.tracer.callstack)-1].Calls
	callframe := lastCallStack[len(lastCallStack)-1]
	// record gas used
	l.recordBalanceChange(callframe.From, big.NewInt(0).Neg(big.NewInt(int64(gasUsed))))

	switch callframe.Type {
	case vm.CREATE, vm.CREATE2:
		// record the code
		contract := *callframe.To
		l.recordCode(contract, callframe.Input)
	case vm.SELFDESTRUCT:
		// destruct this contract. code is empty and balance is zero
		contract := *callframe.To
		l.recordCode(contract, []byte{})
		l.recordBalanceChange(contract, big.NewInt(0).Neg(l.env.StateDB.GetBalance(contract)))
	}
}

func (l *StateDiffTracer) CaptureFault(pc uint64, op vm.OpCode, gas, cost uint64, scope *vm.ScopeContext, depth int, err error) {
	l.tracer.CaptureFault(pc, op, gas, cost, scope, depth, err)
}

func (l *StateDiffTracer) CaptureState(pc uint64, op vm.OpCode, gas, cost uint64, scope *vm.ScopeContext, rData []byte, depth int, err error) {
	if op == vm.SSTORE {
		contract := scope.Contract
		stack := scope.Stack
		stackLen := len(stack.Data())
		if stackLen >= 2 {
			value := common.Hash(stack.Data()[stackLen-2].Bytes32())
			address := common.Hash(stack.Data()[stackLen-1].Bytes32())
			l.recordStorage(contract.Address(), address, value)
		}
	}
}

func (l *StateDiffTracer) GetResult() (json.RawMessage, error) {
	return nil, nil
}

func (l *StateDiffTracer) Stop(err error) {

}

func (l *StateDiffTracer) tryInitAccDiff(addr common.Address) bool {
	if _, ok := l.accounts[addr]; !ok {
		l.accounts[addr] = accountDiff{
			balanceDelta: big.NewInt(0),
			storage:      make(map[common.Hash]diff[common.Hash]),
			code:         diff[[]byte]{nil, nil},
		}
		return true
	}
	return false
}

func (l *StateDiffTracer) recordNonceIncrese(addr common.Address) {
	l.tryInitAccDiff(addr)
	diff := l.accounts[addr]
	diff.nonceDelta++
	l.accounts[addr] = diff
}

func (l *StateDiffTracer) recordCode(addr common.Address, code []byte) {
	isInit := l.tryInitAccDiff(addr)
	diff := l.accounts[addr]
	if isInit {
		// init non-nil code before change
		beforeCode := l.env.StateDB.GetCode(addr)
		if beforeCode == nil {
			beforeCode = []byte{}
		}
		diff.code.before = beforeCode
	}

	diff.code.after = code
	l.accounts[addr] = diff
}

func (l *StateDiffTracer) recordStorage(addr common.Address, key, after common.Hash) {
	isInit := l.tryInitAccDiff(addr)
	value := l.accounts[addr].storage[key]
	value.after = after
	if isInit {
		// take only the initial value
		value.before = l.env.StateDB.GetState(addr, key)
	}
	l.accounts[addr].storage[key] = value
}

// update balance
func (l *StateDiffTracer) recordBalanceChange(addr common.Address, delta *big.Int) {
	l.tryInitAccDiff(addr)
	diff := l.accounts[addr]
	diff.balanceDelta.Add(diff.balanceDelta, delta)
	l.accounts[addr] = diff
}
