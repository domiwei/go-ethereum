package eth

import (
	"context"
	"encoding/json"

	"github.com/ethereum/go-ethereum/eth/tracers"
	"github.com/ethereum/go-ethereum/internal/ethapi"
	"github.com/ethereum/go-ethereum/rpc"
)

type TraceAPI struct {
	backend   *EthAPIBackend
	tracerAPI *tracers.API
}

func NewTraceAPI(b *EthAPIBackend) *TraceAPI {
	return &TraceAPI{
		backend:   b,
		tracerAPI: tracers.NewAPI(b),
	}
}

// CallMany simulate a series of transactions in latest block
func (api *TraceAPI) CallMany(ctx context.Context, txs []ethapi.TransactionArgs) (map[string]interface{}, error) {
	// get latest block number
	latestBlockNumOrHash := rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber)
	// prepare stateDiff tracer
	tracerName := "stateDiffTracer"
	config := tracers.TraceCallConfig{
		TraceConfig: tracers.TraceConfig{
			Tracer:       &tracerName,
			TracerConfig: json.RawMessage("{\"onlyTopCall\": false, \"withLog\": false}"),
		},
	}
	// trace
	traceResult, err := api.tracerAPI.TraceCallMany(ctx, txs, latestBlockNumOrHash, &config)
	if err != nil {
		return nil, err
	}
	result := map[string]interface{}{
		"blockNumber": latestBlockNumOrHash.BlockNumber.String(),
		"traceResult": traceResult,
	}
	return result, nil
}
