package transaction

import (
	"encoding/hex"
	"fmt"
	"github.com/MinterTeam/minter-go-node/core/code"
	"github.com/MinterTeam/minter-go-node/core/commissions"
	"github.com/MinterTeam/minter-go-node/core/state"
	"github.com/MinterTeam/minter-go-node/core/types"
	"github.com/MinterTeam/minter-go-node/formula"
	"github.com/tendermint/tendermint/libs/kv"
	"math/big"
)

type RecreateCoinData struct {
	Symbol               types.CoinSymbol
	InitialAmount        *big.Int
	InitialReserve       *big.Int
	ConstantReserveRatio uint
	MaxSupply            *big.Int
}

func (data RecreateCoinData) TotalSpend(tx *Transaction, context *state.CheckState) (TotalSpends, []Conversion, *big.Int, *Response) {
	panic("implement me")
}

func (data RecreateCoinData) BasicCheck(tx *Transaction, context *state.CheckState) *Response {
	if data.InitialReserve == nil || data.InitialAmount == nil || data.MaxSupply == nil {
		return &Response{
			Code: code.DecodeError,
			Log:  "Incorrect tx data"}
	}

	if data.ConstantReserveRatio < 10 || data.ConstantReserveRatio > 100 {
		return &Response{
			Code: code.WrongCrr,
			Log:  fmt.Sprintf("Constant Reserve Ratio should be between 10 and 100")}
	}

	if data.InitialAmount.Cmp(minCoinSupply) == -1 || data.InitialAmount.Cmp(data.MaxSupply) == 1 {
		return &Response{
			Code: code.WrongCoinSupply,
			Log:  fmt.Sprintf("Coin supply should be between %s and %s", minCoinSupply.String(), data.MaxSupply.String())}
	}

	if data.MaxSupply.Cmp(maxCoinSupply) == 1 {
		return &Response{
			Code: code.WrongCoinSupply,
			Log:  fmt.Sprintf("Max coin supply should be less than %s", maxCoinSupply)}
	}

	if data.InitialReserve.Cmp(minCoinReserve) == -1 {
		return &Response{
			Code: code.WrongCoinSupply,
			Log:  fmt.Sprintf("Coin reserve should be greater than or equal to %s", minCoinReserve.String())}
	}

	sender, _ := tx.Sender()

	coin := context.Coins().GetCoinBySymbol(data.Symbol)
	if coin == nil {
		return &Response{
			Code: code.CoinNotExists,
			Log:  fmt.Sprintf("Coin %s not exists", data.Symbol),
		}
	}

	symbolInfo := context.Coins().GetSymbolInfo(coin.Symbol())
	if symbolInfo == nil || symbolInfo.OwnerAddress() == nil || symbolInfo.OwnerAddress().Compare(sender) != 0 {
		return &Response{
			Code: code.IsNotOwnerOfCoin,
			Log:  "Sender is not owner of coin",
		}
	}

	return nil
}

func (data RecreateCoinData) String() string {
	return fmt.Sprintf("RECREATE COIN symbol:%s reserve:%s amount:%s crr:%d",
		data.Symbol.String(), data.InitialReserve, data.InitialAmount, data.ConstantReserveRatio)
}

func (data RecreateCoinData) Gas() int64 {
	return commissions.RecreateCoin
}

func (data RecreateCoinData) Run(tx *Transaction, context state.Interface, rewardPool *big.Int, currentBlock uint64) Response {
	sender, _ := tx.Sender()

	var checkState *state.CheckState
	var isCheck bool
	if checkState, isCheck = context.(*state.CheckState); !isCheck {
		checkState = state.NewCheckState(context.(*state.State))
	}

	response := data.BasicCheck(tx, checkState)
	if response != nil {
		return *response
	}

	commissionInBaseCoin := tx.CommissionInBaseCoin()
	commission := big.NewInt(0).Set(commissionInBaseCoin)

	if tx.GasCoin != types.GetBaseCoinID() {
		gasCoin := checkState.Coins().GetCoin(tx.GasCoin)

		errResp := CheckReserveUnderflow(gasCoin, commissionInBaseCoin)
		if errResp != nil {
			return *errResp
		}

		if gasCoin.Reserve().Cmp(commissionInBaseCoin) < 0 {
			return Response{
				Code: code.CoinReserveNotSufficient,
				Log:  fmt.Sprintf("Gas coin reserve balance is not sufficient for transaction. Has: %s %s, required %s %s", gasCoin.Reserve().String(), types.GetBaseCoin(), commissionInBaseCoin.String(), types.GetBaseCoin()),
				Info: EncodeError(map[string]string{
					"has_value":      gasCoin.Reserve().String(),
					"required_value": commissionInBaseCoin.String(),
					"gas_coin":       gasCoin.GetFullSymbol(),
				}),
			}
		}

		commission = formula.CalculateSaleAmount(gasCoin.Volume(), gasCoin.Reserve(), gasCoin.Crr(), commissionInBaseCoin)
	}

	if checkState.Accounts().GetBalance(sender, tx.GasCoin).Cmp(commission) < 0 {
		gasCoin := checkState.Coins().GetCoin(tx.GasCoin)

		return Response{
			Code: code.InsufficientFunds,
			Log:  fmt.Sprintf("Insufficient funds for sender account: %s. Wanted %s %s", sender.String(), commission.String(), gasCoin.GetFullSymbol()),
			Info: EncodeError(map[string]string{
				"sender":       sender.String(),
				"needed_value": commission.String(),
				"gas_coin":     gasCoin.GetFullSymbol(),
			}),
		}
	}

	if checkState.Accounts().GetBalance(sender, types.GetBaseCoinID()).Cmp(data.InitialReserve) < 0 {
		return Response{
			Code: code.InsufficientFunds,
			Log:  fmt.Sprintf("Insufficient funds for sender account: %s. Wanted %s %s", sender.String(), data.InitialReserve.String(), types.GetBaseCoin()),
			Info: EncodeError(map[string]string{
				"sender":         sender.String(),
				"needed_reserve": data.InitialReserve.String(),
				"base_coin":      fmt.Sprintf("%s", types.GetBaseCoin()),
			}),
		}
	}

	if tx.GasCoin.IsBaseCoin() {
		gasCoin := checkState.Coins().GetCoin(tx.GasCoin)

		totalTxCost := big.NewInt(0)
		totalTxCost.Add(totalTxCost, data.InitialReserve)
		totalTxCost.Add(totalTxCost, commission)

		if checkState.Accounts().GetBalance(sender, types.GetBaseCoinID()).Cmp(totalTxCost) < 0 {
			return Response{
				Code: code.InsufficientFunds,
				Log:  fmt.Sprintf("Insufficient funds for sender account: %s. Wanted %s %s", sender.String(), totalTxCost.String(), gasCoin.GetFullSymbol()),
				Info: EncodeError(map[string]string{
					"sender":       sender.String(),
					"needed_value": totalTxCost.String(),
					"gas_coin":     gasCoin.GetFullSymbol(),
				}),
			}
		}
	}

	tags := kv.Pairs{
		kv.Pair{Key: []byte("tx.type"), Value: []byte(hex.EncodeToString([]byte{byte(TypeRecreateCoin)}))},
		kv.Pair{Key: []byte("tx.from"), Value: []byte(hex.EncodeToString(sender[:]))},
	}

	if deliverState, ok := context.(*state.State); ok {
		rewardPool.Add(rewardPool, commissionInBaseCoin)

		deliverState.Coins.SubReserve(tx.GasCoin, commissionInBaseCoin)
		deliverState.Coins.SubVolume(tx.GasCoin, commission)

		deliverState.Accounts.SubBalance(sender, types.GetBaseCoinID(), data.InitialReserve)
		deliverState.Accounts.SubBalance(sender, tx.GasCoin, commission)

		coinID := deliverState.App.GetNextCoinID()
		deliverState.Coins.Recreate(
			coinID,
			data.Symbol,
			data.InitialAmount,
			data.ConstantReserveRatio,
			data.InitialReserve,
			data.MaxSupply,
		)

		deliverState.App.SetCoinsCount(coinID.Uint32())
		deliverState.Accounts.AddBalance(sender, coinID, data.InitialAmount)
		deliverState.Accounts.SetNonce(sender, tx.Nonce)

		tags = append(tags, kv.Pair{
			Key:   []byte("tx.coin"),
			Value: []byte(data.Symbol.String()),
		})
	}

	return Response{
		Code:      code.OK,
		Tags:      tags,
		GasUsed:   tx.Gas(),
		GasWanted: tx.Gas(),
	}
}