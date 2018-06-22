// Copyright (c) 2018 IoTeX
// This is an alpha (internal) release and is not suitable for production. This source code is provided ‘as is’ and no
// warranties are given as to title or non-infringement, merchantability or fitness for purpose and, to the extent
// permitted by law, all liability for your use of the code is disclaimed. This source code is governed by Apache
// License 2.0 that can be found in the LICENSE file.

package e2etests

import (
	"encoding/hex"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/iotexproject/iotex-core-internal/blockchain"
	"github.com/iotexproject/iotex-core-internal/blockchain/action"
	"github.com/iotexproject/iotex-core-internal/config"
	"github.com/iotexproject/iotex-core-internal/iotxaddress"
	"github.com/iotexproject/iotex-core-internal/network"
	pb "github.com/iotexproject/iotex-core-internal/proto"
	"github.com/iotexproject/iotex-core-internal/server/itx"
	"github.com/iotexproject/iotex-core-internal/test/util"
)

const (
	// Sender's public/private key pair
	fromPubKey  = "4104b381ac1ace9c139ecae6850e6e1a48bcd9254cfa2359fc1d8b1098e4914b2ca2b4033218c95e638a38de09d005ce1cc45bd0a555865e8552f9cb7b8c1fe5916285310c0cf502"
	fromPrivKey = "af149653127da7c46b4dc5ef9adf7a07405fe9962756185ec47849fe6e5093d7d3c21d01"
	// Recipient's public/private key pair
	toPubKey  = "734b0ce05a018f2aefc13c832cca64ba58b10ebbdc5bc3b6a549ab28bc08530e56e74002673aafbc6fc136aab63874318c8a2a5b68c6b53f2b9a7acd54996bdcd70a2fc72241f307"
	toPrivKey = "a8cf5a40a7b76ed93433f4f92fe9a7140e5c3309769b188c647d1eecf9e1e6eedd0e5600"
)

func TestLocalActPool(t *testing.T) {
	require := require.New(t)

	cfg, err := config.LoadConfigWithPathWithoutValidation(localTestConfigPath)
	require.Nil(err)
	cfg.Network.BootstrapNodes = []string{"127.0.0.1:10000"}

	util.CleanupPath(t, testTriePath)
	defer util.CleanupPath(t, testTriePath)
	util.CleanupPath(t, testDBPath)
	defer util.CleanupPath(t, testDBPath)

	cfg.Chain.TrieDBPath = testTriePath
	cfg.Chain.InMemTest = false
	cfg.Chain.ChainDBPath = testDBPath
	cfg.Consensus.Scheme = config.StandaloneScheme
	cfg.Delegate.Addrs = []string{"127.0.0.1:10000"}

	blockchain.Gen.TotalSupply = uint64(50 << 22)
	blockchain.Gen.BlockReward = uint64(0)

	// create node
	svr := itx.NewServer(*cfg)
	err = svr.Init()
	require.Nil(err)
	err = svr.Start()
	require.Nil(err)
	defer svr.Stop()

	bc := svr.Bc()
	require.NotNil(bc)
	t.Log("Create blockchain pass")

	ap := svr.Ap()
	require.NotNil(ap)

	p2 := svr.P2p()
	require.NotNil(p2)

	p1 := network.NewOverlay(&cfg.Network)
	require.NotNil(p1)
	p1.PRC.Addr = "127.0.0.1:10001"
	p1.Init()
	p1.Start()
	defer p1.Stop()

	from := constructAddress(fromPubKey, fromPrivKey)
	to := constructAddress(toPubKey, toPrivKey)

	// Create three valid actions from "from" to "to"
	tsf1, _ := signedTransfer(from, to, uint64(1), big.NewInt(1))
	vote2, _ := signedVote(from, to, uint64(2))
	tsf3, _ := signedTransfer(from, to, uint64(3), big.NewInt(3))

	// Create three invalid actions from "from" to "to"
	// Existed Vote
	vote4, _ := signedVote(from, to, uint64(2))
	// Coinbase Transfer
	tsf5, _ := signedTransfer(from, to, uint64(5), big.NewInt(5))
	tsf5.IsCoinbase = true
	// Unsigned Vote
	vote6 := action.NewVote(uint64(6), from.PublicKey, to.PublicKey)

	// Wrap transfers and votes as actions
	act1 := &pb.ActionPb{&pb.ActionPb_Transfer{tsf1.ConvertToTransferPb()}}
	act2 := &pb.ActionPb{&pb.ActionPb_Vote{vote2.ConvertToVotePb()}}
	act3 := &pb.ActionPb{&pb.ActionPb_Transfer{tsf3.ConvertToTransferPb()}}
	act4 := &pb.ActionPb{&pb.ActionPb_Vote{vote4.ConvertToVotePb()}}
	act5 := &pb.ActionPb{&pb.ActionPb_Transfer{tsf5.ConvertToTransferPb()}}
	act6 := &pb.ActionPb{&pb.ActionPb_Vote{vote6.ConvertToVotePb()}}

	// Wait until actions can be successfully broadcasted
	err = util.WaitUntil(10*time.Millisecond, 2*time.Second, func() (bool, error) {
		if err := p1.Broadcast(act1); err != nil {
			return false, err
		}
		transfers, _ := ap.PickActs()
		return len(transfers) == 1, nil
	})
	p1.Broadcast(act2)
	p1.Broadcast(act3)
	p1.Broadcast(act4)
	p1.Broadcast(act5)
	p1.Broadcast(act6)
	// Wait until actpool is reset
	err = util.WaitUntil(10*time.Millisecond, 5*time.Second, func() (bool, error) {
		transfers, votes := ap.PickActs()
		return len(transfers)+len(votes) == 0, nil
	})
	// Check whether there is a committed block containing all the valid actions picked from actpool
	height, err := bc.TipHeight()
	require.Nil(err)
	blk := &blockchain.Block{}
	for h := height; h > 0; h-- {
		blk, err = bc.GetBlockByHeight(h)
		require.Nil(err)
		if len(blk.Votes) > 0 {
			break
		}
	}
	// Take coinbase transfer into account, there should be 3 transfers in the block
	require.Equal(3, len(blk.Transfers))
	require.Equal(1, len(blk.Votes))
}

func TestPressureActPool(t *testing.T) {
	require := require.New(t)

	cfg, err := config.LoadConfigWithPathWithoutValidation(localTestConfigPath)
	require.Nil(err)
	cfg.Network.BootstrapNodes = []string{"127.0.0.1:10000"}

	util.CleanupPath(t, testTriePath)
	defer util.CleanupPath(t, testTriePath)
	util.CleanupPath(t, testDBPath)
	defer util.CleanupPath(t, testDBPath)

	cfg.Chain.TrieDBPath = testTriePath
	cfg.Chain.InMemTest = false
	cfg.Chain.ChainDBPath = testDBPath
	cfg.Consensus.Scheme = config.StandaloneScheme
	cfg.Delegate.Addrs = []string{"127.0.0.1:10000"}

	blockchain.Gen.TotalSupply = uint64(50 << 22)
	blockchain.Gen.BlockReward = uint64(0)

	// create node
	svr := itx.NewServer(*cfg)
	err = svr.Init()
	require.Nil(err)
	err = svr.Start()
	require.Nil(err)
	defer svr.Stop()

	bc := svr.Bc()
	require.NotNil(bc)
	t.Log("Create blockchain pass")

	ap := svr.Ap()
	require.NotNil(ap)

	p2 := svr.P2p()
	require.NotNil(p2)

	p1 := network.NewOverlay(&cfg.Network)
	require.NotNil(p1)
	p1.PRC.Addr = "127.0.0.1:10001"
	p1.Init()
	p1.Start()
	defer p1.Stop()

	from := constructAddress(fromPubKey, fromPrivKey)
	to := constructAddress(toPubKey, toPrivKey)

	// Create 1000 valid transfers and broadcast
	tsf1, _ := signedTransfer(from, to, uint64(1), big.NewInt(1))
	// Wrap transfers and votes as actions
	act1 := &pb.ActionPb{&pb.ActionPb_Transfer{tsf1.ConvertToTransferPb()}}

	// Wait until transfers can be successfully broadcasted
	err = util.WaitUntil(10*time.Millisecond, 2*time.Second, func() (bool, error) {
		if err := p1.Broadcast(act1); err != nil {
			return false, err
		}
		transfers, _ := ap.PickActs()
		return len(transfers) == 1, nil
	})
	for i := 2; i <= 1000; i++ {
		tsf, _ := signedTransfer(from, to, uint64(i), big.NewInt(int64(i)))
		act := &pb.ActionPb{&pb.ActionPb_Transfer{tsf.ConvertToTransferPb()}}
		p1.Broadcast(act)
	}

	// Wait until actpool is reset
	err = util.WaitUntil(10*time.Millisecond, 5*time.Second, func() (bool, error) {
		transfers, votes := ap.PickActs()
		return len(transfers)+len(votes) == 0, nil
	})
	// Check whether there is a committed block containing all the valid actions picked from actpool
	height, err := bc.TipHeight()
	require.Nil(err)
	blk := &blockchain.Block{}
	for h := height; h > 0; h-- {
		blk, err = bc.GetBlockByHeight(h)
		require.Nil(err)
		if len(blk.Transfers) > 1 {
			break
		}
	}
	// Take coinbase transfer into account, there should be 257 transfers in the block
	// because every account can hold up to 256 actions in actpool
	require.Equal(257, len(blk.Transfers))
}

// Helper function to return iotex addresses
func constructAddress(pubkey, prikey string) *iotxaddress.Address {
	pubk, err := hex.DecodeString(pubkey)
	if err != nil {
		panic(err)
	}
	prik, err := hex.DecodeString(prikey)
	if err != nil {
		panic(err)
	}
	addr, err := iotxaddress.GetAddress(pubk, iotxaddress.IsTestnet, iotxaddress.ChainID)
	if err != nil {
		panic(err)
	}
	addr.PrivateKey = prik
	return addr
}

// Helper function to return a signed transfer
func signedTransfer(sender *iotxaddress.Address, recipient *iotxaddress.Address, nonce uint64, amount *big.Int) (*action.Transfer, error) {
	transfer := action.NewTransfer(nonce, amount, sender.RawAddress, recipient.RawAddress)
	return transfer.Sign(sender)
}

// Helper function to return a signed vote
func signedVote(voter *iotxaddress.Address, votee *iotxaddress.Address, nonce uint64) (*action.Vote, error) {
	vote := action.NewVote(nonce, voter.PublicKey, votee.PublicKey)
	return vote.Sign(voter)
}