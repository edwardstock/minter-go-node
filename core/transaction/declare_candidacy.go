package transaction

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/MinterTeam/minter-go-node/core/code"
	"github.com/MinterTeam/minter-go-node/core/commissions"
	"github.com/MinterTeam/minter-go-node/core/state"
	"github.com/MinterTeam/minter-go-node/core/types"
	"github.com/MinterTeam/minter-go-node/core/validators"
	"github.com/MinterTeam/minter-go-node/formula"
	"github.com/tendermint/tendermint/libs/kv"
	"math/big"
	"strconv"
)

const minCommission = 0
const maxCommission = 100

type DeclareCandidacyData struct {
	Address    types.Address
	PubKey     types.Pubkey
	Commission uint
	Coin       types.CoinSymbol
	Stake      *big.Int
}

func (data DeclareCandidacyData) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Address    string `json:"address"`
		PubKey     string `json:"pub_key"`
		Commission string `json:"commission"`
		Coin       string `json:"coin"`
		Stake      string `json:"stake"`
	}{
		Address:    data.Address.String(),
		PubKey:     data.PubKey.String(),
		Commission: strconv.Itoa(int(data.Commission)),
		Coin:       data.Coin.String(),
		Stake:      data.Stake.String(),
	})
}

func (data DeclareCandidacyData) TotalSpend(tx *Transaction, context *state.State) (TotalSpends, []Conversion, *big.Int, *Response) {
	panic("implement me")
}

func (data DeclareCandidacyData) BasicCheck(tx *Transaction, context *state.State) *Response {
	if data.Stake == nil {
		return &Response{
			Code: code.DecodeError,
			Log:  "Incorrect tx data"}
	}

	if !context.Coins.Exists(data.Coin) {
		return &Response{
			Code: code.CoinNotExists,
			Log:  fmt.Sprintf("Coin %s not exists", data.Coin),
			Info: EncodeError(map[string]string{
				"coin": fmt.Sprintf("%s", data.Coin),
			}),
		}
	}

	if context.Candidates.Exists(data.PubKey) {
		return &Response{
			Code: code.CandidateExists,
			Log:  fmt.Sprintf("Candidate with such public key (%s) already exists", data.PubKey.String()),
			Info: EncodeError(map[string]string{
				"public_key": data.PubKey.String(),
			}),
		}
	}

	if data.Commission < minCommission || data.Commission > maxCommission {
		return &Response{
			Code: code.WrongCommission,
			Log:  fmt.Sprintf("Commission should be between 0 and 100"),
			Info: EncodeError(map[string]string{
				"got_commission": fmt.Sprintf("%d", data.Commission),
			}),
		}
	}

	return nil
}

func (data DeclareCandidacyData) String() string {
	return fmt.Sprintf("DECLARE CANDIDACY address:%s pubkey:%s commission: %d",
		data.Address.String(), data.PubKey.String(), data.Commission)
}

func (data DeclareCandidacyData) Gas() int64 {
	return commissions.DeclareCandidacyTx
}

func (data DeclareCandidacyData) Run(tx *Transaction, context *state.State, isCheck bool, rewardPool *big.Int, currentBlock uint64) Response {
	sender, _ := tx.Sender()

	response := data.BasicCheck(tx, context)
	if response != nil {
		return *response
	}

	maxCandidatesCount := validators.GetCandidatesCountForBlock(currentBlock)

	if context.Candidates.Count() >= maxCandidatesCount && !context.Candidates.IsNewCandidateStakeSufficient(data.Coin, data.Stake, maxCandidatesCount) {
		return Response{
			Code: code.TooLowStake,
			Log:  fmt.Sprintf("Given stake is too low")}
	}

	commissionInBaseCoin := big.NewInt(0).Mul(big.NewInt(int64(tx.GasPrice)), big.NewInt(tx.Gas()))
	commissionInBaseCoin.Mul(commissionInBaseCoin, CommissionMultiplier)
	commission := big.NewInt(0).Set(commissionInBaseCoin)

	if !tx.GasCoin.IsBaseCoin() {
		coin := context.Coins.GetCoin(tx.GasCoin)

		errResp := CheckReserveUnderflow(coin, commissionInBaseCoin)
		if errResp != nil {
			return *errResp
		}

		if coin.Reserve().Cmp(commissionInBaseCoin) < 0 {
			return Response{
				Code: code.CoinReserveNotSufficient,
				Log:  fmt.Sprintf("Coin reserve balance is not sufficient for transaction. Has: %s, required %s", coin.Reserve().String(), commissionInBaseCoin.String()),
				Info: EncodeError(map[string]string{
					"has_reserve": coin.Reserve().String(),
					"commission":  commissionInBaseCoin.String(),
					"gas_coin":    coin.CName,
				}),
			}
		}

		commission = formula.CalculateSaleAmount(coin.Volume(), coin.Reserve(), coin.Crr(), commissionInBaseCoin)
	}

	if context.Accounts.GetBalance(sender, data.Coin).Cmp(data.Stake) < 0 {
		return Response{
			Code: code.InsufficientFunds,
			Log:  fmt.Sprintf("Insufficient funds for sender account: %s. Wanted %s %s", sender.String(), data.Stake, data.Coin),
			Info: EncodeError(map[string]string{
				"sender":       sender.String(),
				"needed_value": data.Stake.String(),
				"coin":         fmt.Sprintf("%s", data.Coin),
			}),
		}
	}

	if context.Accounts.GetBalance(sender, tx.GasCoin).Cmp(commission) < 0 {
		return Response{
			Code: code.InsufficientFunds,
			Log:  fmt.Sprintf("Insufficient funds for sender account: %s. Wanted %s %s", sender.String(), commission, tx.GasCoin),
			Info: EncodeError(map[string]string{
				"sender":       sender.String(),
				"needed_value": commission.String(),
				"gas_coin":     fmt.Sprintf("%s", tx.GasCoin),
			}),
		}
	}

	if data.Coin == tx.GasCoin {
		totalTxCost := big.NewInt(0)
		totalTxCost.Add(totalTxCost, data.Stake)
		totalTxCost.Add(totalTxCost, commission)

		if context.Accounts.GetBalance(sender, tx.GasCoin).Cmp(totalTxCost) < 0 {
			return Response{
				Code: code.InsufficientFunds,
				Log:  fmt.Sprintf("Insufficient funds for sender account: %s. Wanted %s %s", sender.String(), totalTxCost.String(), tx.GasCoin),
				Info: EncodeError(map[string]string{
					"sender":       sender.String(),
					"needed_value": totalTxCost.String(),
					"gas_coin":     fmt.Sprintf("%s", tx.GasCoin),
				}),
			}
		}
	}

	if !isCheck {
		rewardPool.Add(rewardPool, commissionInBaseCoin)

		context.Coins.SubReserve(tx.GasCoin, commissionInBaseCoin)
		context.Coins.SubVolume(tx.GasCoin, commission)

		context.Accounts.SubBalance(sender, data.Coin, data.Stake)
		context.Accounts.SubBalance(sender, tx.GasCoin, commission)
		context.Candidates.Create(data.Address, sender, data.PubKey, data.Commission)
		context.Candidates.Delegate(sender, data.PubKey, data.Coin, data.Stake, big.NewInt(0))
		context.Accounts.SetNonce(sender, tx.Nonce)
	}

	tags := kv.Pairs{
		kv.Pair{Key: []byte("tx.type"), Value: []byte(hex.EncodeToString([]byte{byte(TypeDeclareCandidacy)}))},
		kv.Pair{Key: []byte("tx.from"), Value: []byte(hex.EncodeToString(sender[:]))},
	}

	return Response{
		Code:      code.OK,
		GasUsed:   tx.Gas(),
		GasWanted: tx.Gas(),
		Tags:      tags,
	}
}
