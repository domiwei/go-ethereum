package native

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
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

func (l *StateDiffTracer) CaptureTxStart(gasLimit uint64) {
	l.tracer.CaptureTxStart(gasLimit)
}

func (l *StateDiffTracer) CaptureTxEnd(restGas uint64) {
	l.tracer.CaptureTxEnd(restGas)
	callFrame := l.tracer.callstack[0]
	caller := callFrame.From
	used := callFrame.GasUsed
	// record gas used here instead of capture whenever gas is used, because need to consider intrinsic gas
	l.recordBalanceChange(caller, big.NewInt(-int64(used)))
	// additional nonce increment when first call is not CREATE
	if callFrame.Type != vm.CREATE {
		l.recordNonceIncrese(caller)
	}
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
	// Note: do not record gasUsed here. All gas used value is recorded in TxEnd

	opType := callframe.Type
	switch opType {
	case vm.CREATE, vm.CREATE2, vm.CALL:
		if opType == vm.CREATE || opType == vm.CREATE2 {
			// record the code
			contract := *callframe.To
			l.recordCode(contract, l.env.StateDB.GetCode(contract))
		}
		// ether transfer
		value := callframe.Value
		if value != nil {
			from := callframe.From
			to := *callframe.To
			l.recordBalanceChange(from, big.NewInt(0).Neg(value))
			l.recordBalanceChange(to, value)
		}
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
	// Note: do not record gasUsed here. All gas used value is recorded in TxEnd

	opType := callframe.Type
	switch opType {
	case vm.CREATE, vm.CREATE2, vm.CALL:
		if opType == vm.CREATE || opType == vm.CREATE2 {
			// record the code
			contract := *callframe.To
			l.recordCode(contract, callframe.Input)
		}
		// ether transfer
		value := callframe.Value
		if value != nil {
			from := callframe.From
			to := *callframe.To
			l.recordBalanceChange(from, big.NewInt(0).Neg(value))
			l.recordBalanceChange(to, value)
		}
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
			// record storage change
			l.recordStorage(contract.Address(), address, value)
		}
	}
}

func (l *StateDiffTracer) GetResult() (json.RawMessage, error) {
	stateDiffResult := map[string]accountReport{}
	for addr, diff := range l.accounts {
		stateDiffResult[addr.Hex()] = l.report(addr, diff)
	}
	result := map[string]interface{}{
		// only stateDiff result is supported now
		"stateDiff": stateDiffResult,
	}
	return json.Marshal(result)
}

func (l *StateDiffTracer) Stop(err error) {
	l.tracer.Stop(err)
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

type accountReport struct {
	Balance any               `json:"balance"`
	Nonce   any               `json:"nonce"`
	Code    any               `json:"code"`
	Storage map[string]fromTo `json:"storage"`
}
type fromTo struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func (l *StateDiffTracer) report(addr common.Address, a accountDiff) accountReport {
	result := accountReport{
		Balance: "=",
		Nonce:   "=",
		Code:    "=",
		Storage: make(map[string]fromTo),
	}
	// balance
	if a.balanceDelta != nil && a.balanceDelta.Sign() != 0 {
		delta := a.balanceDelta
		current := l.env.StateDB.GetBalance(addr)
		result.Balance = fromTo{
			// from = current - delta. transform to hex
			From: fmt.Sprintf("0x%x", big.NewInt(0).Sub(current, delta).Text(16)),
			To:   fmt.Sprintf("0x%x", current.Text(16)),
		}
	}
	// nonce
	if a.nonceDelta != 0 {
		current := l.env.StateDB.GetNonce(addr)
		result.Nonce = fromTo{
			// in hex
			From: fmt.Sprintf("0x%x", current-uint64(a.nonceDelta)),
			To:   fmt.Sprintf("0x%x", current),
		}
	}
	// code
	if a.code.before != nil || a.code.after != nil {
		before, after := "", ""
		if a.code.before != nil {
			before = hex.EncodeToString(a.code.before)
		}
		if a.code.after != nil {
			after = hex.EncodeToString(a.code.after)
		}
		if before != after {
			result.Code = fromTo{
				From: before,
				To:   after,
			}
		}
	}
	// storage
	for k, v := range a.storage {
		before := v.before.Hex()
		after := v.after.Hex()
		if before != after {
			result.Storage[k.Hex()] = fromTo{
				From: before,
				To:   after,
			}
		}
	}
	return result
}
