package contract

import (
	"context"
	"fmt"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/republicprotocol/renex-ingress-go/contract/bindings"
)

// CREATE TABLE withdrawals (
//     hash        bytea,
//     address     varchar(42),
//     token       int,
// 	amount      varchar,
//     timestamp   bigint,
//     nonce       int
// );

// Binder implements all methods that will communicate with the smart contracts
type Binder struct {
	mu           *sync.RWMutex
	network      Network
	conn         Conn
	transactOpts *bind.TransactOpts
	callOpts     *bind.CallOpts

	renExBrokerVerifier *bindings.RenExBrokerVerifier
	renExSettlement     *bindings.RenExSettlement
	orderbook           *bindings.Orderbook
	wyre                *bindings.Wyre
}

// NewBinder returns a Binder to communicate with contracts
func NewBinder(auth *bind.TransactOpts, conn Conn) (Binder, error) {
	transactOpts := *auth
	transactOpts.GasPrice = big.NewInt(5000000000)

	nonce, err := conn.Client.PendingNonceAt(context.Background(), transactOpts.From)
	if err != nil {
		return Binder{}, err
	}
	transactOpts.Nonce = big.NewInt(int64(nonce))

	renExBrokerVerifier, err := bindings.NewRenExBrokerVerifier(common.HexToAddress(conn.Config.RenExBrokerVerifierAddress), bind.ContractBackend(conn.Client))
	if err != nil {
		fmt.Println(fmt.Errorf("cannot bind to RenExBrokerVerifier: %v", err))
		return Binder{}, err
	}

	orderbook, err := bindings.NewOrderbook(common.HexToAddress(conn.Config.OrderbookAddress), bind.ContractBackend(conn.Client))
	if err != nil {
		fmt.Println(fmt.Errorf("cannot bind to Orderbook: %v", err))
		return Binder{}, err
	}
	settlement, err := bindings.NewRenExSettlement(common.HexToAddress(conn.Config.RenExSettlementAddress), bind.ContractBackend(conn.Client))
	if err != nil {
		fmt.Println(fmt.Errorf("cannot bind to Settlement: %v", err))
		return Binder{}, err
	}

	wyre, err := bindings.NewWyre(common.HexToAddress(conn.Config.WyreAddress), bind.ContractBackend(conn.Client))
	if err != nil {
		fmt.Println(fmt.Errorf("cannot bind to Wyre: %v", err))
		return Binder{}, err
	}

	return Binder{
		mu:           new(sync.RWMutex),
		network:      conn.Config.Network,
		conn:         conn,
		transactOpts: &transactOpts,
		callOpts:     &bind.CallOpts{},

		renExBrokerVerifier: renExBrokerVerifier,
		renExSettlement:     settlement,
		orderbook:           orderbook,
		wyre:                wyre,
	}, nil
}

// GetTraderWithdrawalNonce retrieves the withdrawal nonce for approving a
// trader's withdrawal. A signature can only be used once.
func (binder *Binder) GetTraderWithdrawalNonce(trader common.Address) (*big.Int, error) {
	binder.mu.RLock()
	defer binder.mu.RUnlock()

	return binder.getTraderWithdrawalNonce(trader)
}

func (binder *Binder) getTraderWithdrawalNonce(trader common.Address) (*big.Int, error) {
	return binder.renExBrokerVerifier.TraderNonces(binder.callOpts, trader)
}

// BalanceOf retrieves the Wyre KYC verification status of a trader.
func (binder *Binder) BalanceOf(trader common.Address) (*big.Int, error) {
	binder.mu.RLock()
	defer binder.mu.RUnlock()

	return binder.balanceOf(trader)
}

func (binder *Binder) balanceOf(trader common.Address) (*big.Int, error) {
	return binder.wyre.BalanceOf(binder.callOpts, trader)
}

// GetOrderTrader of the given order id.
func (binder *Binder) GetOrderTrader(orderID [32]byte) (common.Address, error) {
	return binder.orderbook.OrderTrader(&bind.CallOpts{}, orderID)
}

// func (binder *Binder) WatchLogOrderSettled(ids [][32]byte) (chan *bindings.RenExSettlementLogOrderSettled, error) {
// 	orderSettled := make(chan *bindings.RenExSettlementLogOrderSettled)
// 	_, err := binder.renExSettlementWs.WatchLogOrderSettled(&bind.WatchOpts{}, orderSettled, ids)
// 	return orderSettled, err
// }

func (binder *Binder) GetMatchDetails(id [32]byte) (struct {
	Settled         bool
	OrderIsBuy      bool
	MatchedID       [32]byte
	PriorityVolume  *big.Int
	SecondaryVolume *big.Int
	PriorityFee     *big.Int
	SecondaryFee    *big.Int
	PriorityToken   uint32
	SecondaryToken  uint32
}, error) {
	return binder.renExSettlement.GetMatchDetails(&bind.CallOpts{}, id)
}

func (binder *Binder) OrderTrader(id [32]byte) (string, error) {
	trader, err := binder.orderbook.OrderTrader(&bind.CallOpts{}, id)
	if err != nil {
		return "", err
	}

	return trader.Hex(), nil
}

func (binder *Binder) OrderState(id [32]byte) (uint8, error) {
	return binder.orderbook.OrderState(&bind.CallOpts{}, id)
}
