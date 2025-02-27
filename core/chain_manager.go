// Copyright 2015 The go-ethereum Authors
// Copyright 2018 Webchain project
// This file is part of Webchain.
//
// Webchain is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// Webchain is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with Webchain. If not, see <http://www.gnu.org/licenses/>.

package core

import (
	"fmt"
	"math/big"

	"github.com/webchain-network/webchaind/common"
	"github.com/webchain-network/webchaind/core/state"
	"github.com/webchain-network/webchaind/core/types"
	"github.com/webchain-network/webchaind/ethdb"
	"github.com/webchain-network/webchaind/event"
	"github.com/webchain-network/webchaind/pow"
)

/*
 * TODO: move this to another package.
 */

// MakeChainConfig returns a new ChainConfig with the ethereum default chain settings.
func MakeChainConfig() *ChainConfig {
	return &ChainConfig{
		Forks: []*Fork{
			{
				Name:  "Homestead",
				Block: big.NewInt(0),
				Features: []*ForkFeature{
					{
						ID: "difficulty",
						Options: ChainFeatureConfigOptions{
							"type": "homestead",
						},
					},
				},
			},
		},
	}
}

// FakePow is a non-validating proof of work implementation.
// It returns true from Verify for any block.
type FakePow struct{}

func (f FakePow) Search(block pow.Block, stop <-chan struct{}, index int) (uint64) {
	return 0
}
func (f FakePow) Verify(block pow.Block) bool { return true }
func (f FakePow) GetHashrate() int64          { return 0 }
func (f FakePow) Turbo(bool)                  {}

// So we can deterministically seed different blockchains
var (
	canonicalSeed = 1
	forkSeed      = 2
)

// BlockGen creates blocks for testing.
// See GenerateChain for a detailed explanation.
type BlockGen struct {
	i       int
	parent  *types.Block
	chain   []*types.Block
	header  *types.Header
	statedb *state.StateDB

	gasPool  *GasPool
	txs      []*types.Transaction
	receipts []*types.Receipt
	uncles   []*types.Header
	config   *ChainConfig
}

// SetCoinbase sets the coinbase of the generated block.
// It can be called at most once.
func (b *BlockGen) SetCoinbase(addr common.Address) {
	if b.gasPool != nil {
		if len(b.txs) > 0 {
			panic("coinbase must be set before adding transactions")
		}
		panic("coinbase can only be set once")
	}
	b.header.Coinbase = addr
	b.gasPool = new(GasPool).AddGas(b.header.GasLimit)
}

// SetExtra sets the extra data field of the generated block.
func (b *BlockGen) SetExtra(data []byte) {
	b.header.Extra = data
}

// AddTx adds a transaction to the generated block. If no coinbase has
// been set, the block's coinbase is set to the zero address.
//
// AddTx panics if the transaction cannot be executed. In addition to
// the protocol-imposed limitations (gas limit, etc.), there are some
// further limitations on the content of transactions that can be
// added. Notably, contract code relying on the BLOCKHASH instruction
// will panic during execution.
func (b *BlockGen) AddTx(tx *types.Transaction) {
	if b.gasPool == nil {
		b.SetCoinbase(common.Address{})
	}
	b.statedb.StartRecord(tx.Hash(), common.Hash{}, len(b.txs))
	receipt, _, _, err := ApplyTransaction(b.config, nil, b.gasPool, b.statedb, b.header, tx, b.header.GasUsed)
	if err != nil {
		panic(err)
	}
	b.txs = append(b.txs, tx)
	b.receipts = append(b.receipts, receipt)
}

// Number returns the block number of the block being generated.
func (b *BlockGen) Number() *big.Int {
	return new(big.Int).Set(b.header.Number)
}

// AddUncheckedReceipts forcefully adds a receipts to the block without a
// backing transaction.
//
// AddUncheckedReceipts will cause consensus failures when used during real
// chain processing. This is best used in conjunction with raw block insertion.
func (b *BlockGen) AddUncheckedReceipt(receipt *types.Receipt) {
	b.receipts = append(b.receipts, receipt)
}

// TxNonce returns the next valid transaction nonce for the
// account at addr. It panics if the account does not exist.
func (b *BlockGen) TxNonce(addr common.Address) uint64 {
	if !b.statedb.Exist(addr) {
		panic("account does not exist")
	}
	return b.statedb.GetNonce(addr)
}

// AddUncle adds an uncle header to the generated block.
func (b *BlockGen) AddUncle(h *types.Header) {
	b.uncles = append(b.uncles, h)
}

// PrevBlock returns a previously generated block by number. It panics if
// num is greater or equal to the number of the block being generated.
// For index -1, PrevBlock returns the parent block given to GenerateChain.
func (b *BlockGen) PrevBlock(index int) *types.Block {
	if index >= b.i {
		panic("block index out of range")
	}
	if index == -1 {
		return b.parent
	}
	return b.chain[index]
}

// OffsetTime modifies the time instance of a block, implicitly changing its
// associated difficulty. It's useful to test scenarios where forking is not
// tied to chain length directly.
func (b *BlockGen) OffsetTime(seconds int64) {
	b.header.Time.Add(b.header.Time, new(big.Int).SetInt64(seconds))
	if b.header.Time.Cmp(b.parent.Header().Time) <= 0 {
		panic("block time out of range")
	}
	b.header.Difficulty = CalcDifficulty(MakeChainConfig(), b.header.Time.Uint64(), b.parent.Time().Uint64(), b.parent.Number(), b.parent.Difficulty())
}

// GenerateChain creates a chain of n blocks. The first block's
// parent will be the provided parent. db is used to store
// intermediate states and should contain the parent's state trie.
//
// The generator function is called with a new block generator for
// every block. Any transactions and uncles added to the generator
// become part of the block. If gen is nil, the blocks will be empty
// and their coinbase will be the zero address.
//
// Blocks created by GenerateChain do not contain valid proof of work
// values. Inserting them into BlockChain requires use of FakePow or
// a similar non-validating proof of work implementation.
func GenerateChain(config *ChainConfig, parent *types.Block, db ethdb.Database, n int, gen func(int, *BlockGen)) ([]*types.Block, []types.Receipts) {
	blocks, receipts := make(types.Blocks, n), make([]types.Receipts, n)
	genblock := func(i int, h *types.Header, statedb *state.StateDB) (*types.Block, types.Receipts) {
		b := &BlockGen{parent: parent, i: i, chain: blocks, header: h, statedb: statedb, config: config}

		// Mutate the state and block according to any hard-fork specs
		if config == nil {
			config = DefaultConfigMainnet.ChainConfig // MakeChainConfig()
		}
		// Execute any user modifications to the block and finalize it
		if gen != nil {
			gen(i, b)
		}
		AccumulateRewards(config, statedb, h, b.uncles)
		root, err := statedb.CommitTo(db, false)
		if err != nil {
			panic(fmt.Sprintf("state write error: %v", err))
		}
		h.Root = root
		return types.NewBlock(h, b.txs, b.uncles, b.receipts), b.receipts
	}
	for i := 0; i < n; i++ {
		statedb, err := state.New(parent.Root(), state.NewDatabase(db))
		if err != nil {
			panic(err)
		}
		header := makeHeader(config, parent, statedb)
		block, receipt := genblock(i, header, statedb)
		blocks[i] = block
		receipts[i] = receipt
		parent = block
	}
	return blocks, receipts
}

func makeHeader(config *ChainConfig, parent *types.Block, state *state.StateDB) *types.Header {
	var time *big.Int
	if parent.Time() == nil {
		time = big.NewInt(10)
	} else {
		time = new(big.Int).Add(parent.Time(), big.NewInt(10)) // block time is fixed at 10 seconds
	}
	return &types.Header{
		Root:       state.IntermediateRoot(false),
		ParentHash: parent.Hash(),
		Coinbase:   parent.Coinbase(),
		Difficulty: CalcDifficulty(config, time.Uint64(), new(big.Int).Sub(time, big.NewInt(10)).Uint64(), parent.Number(), parent.Difficulty()),
		GasLimit:   CalcGasLimit(parent),
		GasUsed:    new(big.Int),
		Number:     new(big.Int).Add(parent.Number(), common.Big1),
		Time:       time,
	}
}

// newCanonical creates a chain database, and injects a deterministic canonical
// chain. Depending on the full flag, if creates either a full block chain or a
// header only chain.
func newCanonical(config *ChainConfig, n int, full bool) (ethdb.Database, *BlockChain, error) {
	// Create the new chain database
	db, err := ethdb.NewMemDatabase()
	if err != nil {
		return nil, nil, err
	}

	evmux := &event.TypeMux{}

	// Initialize a fresh chain with only a genesis block
	genesis, err := WriteGenesisBlock(db, DefaultConfigMorden.Genesis)
	if err != nil {
		return nil, nil, err
	}

	blockchain, err := NewBlockChain(db, MakeChainConfig(), FakePow{}, evmux)
	if err != nil {
		return nil, nil, err
	}

	// Create and inject the requested chain
	if n == 0 {
		return db, blockchain, nil
	}
	if full {
		// Full block-chain requested
		blocks := makeBlockChain(config, genesis, n, db, canonicalSeed)
		res := blockchain.InsertChain(blocks)
		return db, blockchain, res.Error
	}
	// Header-only chain requested
	headers := makeHeaderChain(config, genesis.Header(), n, db, canonicalSeed)
	res := blockchain.InsertHeaderChain(headers, 1)
	return db, blockchain, res.Error
}

// makeHeaderChain creates a deterministic chain of headers rooted at parent.
func makeHeaderChain(config *ChainConfig, parent *types.Header, n int, db ethdb.Database, seed int) []*types.Header {
	blocks := makeBlockChain(config, types.NewBlockWithHeader(parent), n, db, seed)
	headers := make([]*types.Header, len(blocks))
	for i, block := range blocks {
		headers[i] = block.Header()
	}
	return headers
}

// makeBlockChain creates a deterministic chain of blocks rooted at parent.
func makeBlockChain(config *ChainConfig, parent *types.Block, n int, db ethdb.Database, seed int) []*types.Block {
	blocks, _ := GenerateChain(config, parent, db, n, func(i int, b *BlockGen) {
		b.SetCoinbase(common.Address{0: byte(seed), 19: byte(i)})
	})
	return blocks
}
