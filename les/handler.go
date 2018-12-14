//
//
//
//
//
//
//
//
//
//
//
//
//
//
//

//
package les

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"net"
	"sync"
	"time"

	"github.com/5sWind/bgmchain/common"
	"github.com/5sWind/bgmchain/consensus"
	"github.com/5sWind/bgmchain/core"
	"github.com/5sWind/bgmchain/core/state"
	"github.com/5sWind/bgmchain/core/types"
	"github.com/5sWind/bgmchain/bgm"
	"github.com/5sWind/bgmchain/bgm/downloader"
	"github.com/5sWind/bgmchain/bgmdb"
	"github.com/5sWind/bgmchain/event"
	"github.com/5sWind/bgmchain/light"
	"github.com/5sWind/bgmchain/log"
	"github.com/5sWind/bgmchain/p2p"
	"github.com/5sWind/bgmchain/p2p/discover"
	"github.com/5sWind/bgmchain/p2p/discv5"
	"github.com/5sWind/bgmchain/params"
	"github.com/5sWind/bgmchain/rlp"
	"github.com/5sWind/bgmchain/trie"
)

const (
	softResponseLimit = 2 * 1024 * 1024 //
	estHeaderRlpSize  = 500             //

	bgmVersion = 63 //

	MaxHeaderFetch           = 192 //
	MaxBodyFetch             = 32  //
	MaxReceiptFetch          = 128 //
	MaxCodeFetch             = 64  //
	MaxProofsFetch           = 64  //
	MaxHelperTrieProofsFetch = 64  //
	MaxTxSend                = 64  //
	MaxTxStatus              = 256 //

	disableClientRemovePeer = false
)

//
//
var errIncompatibleConfig = errors.New("incompatible configuration")

func errResp(code errCode, format string, v ...interface{}) error {
	return fmt.Errorf("%v - %v", code, fmt.Sprintf(format, v...))
}

type BlockChain interface {
	HasHeader(hash common.Hash, number uint64) bool
	GetHeader(hash common.Hash, number uint64) *types.Header
	GetHeaderByHash(hash common.Hash) *types.Header
	CurrentHeader() *types.Header
	GetTdByHash(hash common.Hash) *big.Int
	InsertHeaderChain(chain []*types.Header, checkFreq int) (int, error)
	Rollback(chain []common.Hash)
	Status() (td *big.Int, currentBlock common.Hash, genesisBlock common.Hash)
	GetHeaderByNumber(number uint64) *types.Header
	GetBlockHashesFromHash(hash common.Hash, max uint64) []common.Hash
	LastBlockHash() common.Hash
	Genesis() *types.Block
	SubscribeChainHeadEvent(ch chan<- core.ChainHeadEvent) event.Subscription
}

type txPool interface {
	AddRemotes(txs []*types.Transaction) []error
	Status(hashes []common.Hash) []core.TxStatus
}

type ProtocolManager struct {
	lightSync   bool
	txpool      txPool
	txrelay     *LesTxRelay
	networkId   uint64
	chainConfig *params.ChainConfig
	blockchain  BlockChain
	chainDb     bgmdb.Database
	odr         *LesOdr
	server      *LesServer
	serverPool  *serverPool
	lesTopic    discv5.Topic
	reqDist     *requestDistributor
	retriever   *retrieveManager

	downloader *downloader.Downloader
	fetcher    *lightFetcher
	peers      *peerSet

	SubProtocols []p2p.Protocol

	eventMux *event.TypeMux

//
	newPeerCh   chan *peer
	quitSync    chan struct{}
	noMorePeers chan struct{}

//
//
	wg *sync.WaitGroup
}

//
//
func NewProtocolManager(chainConfig *params.ChainConfig, lightSync bool, protocolVersions []uint, networkId uint64, mux *event.TypeMux, engine consensus.Engine, peers *peerSet, blockchain BlockChain, txpool txPool, chainDb bgmdb.Database, odr *LesOdr, txrelay *LesTxRelay, quitSync chan struct{}, wg *sync.WaitGroup) (*ProtocolManager, error) {
//
	manager := &ProtocolManager{
		lightSync:   lightSync,
		eventMux:    mux,
		blockchain:  blockchain,
		chainConfig: chainConfig,
		chainDb:     chainDb,
		odr:         odr,
		networkId:   networkId,
		txpool:      txpool,
		txrelay:     txrelay,
		peers:       peers,
		newPeerCh:   make(chan *peer),
		quitSync:    quitSync,
		wg:          wg,
		noMorePeers: make(chan struct{}),
	}
	if odr != nil {
		manager.retriever = odr.retriever
		manager.reqDist = odr.retriever.dist
	}

//
	manager.SubProtocols = make([]p2p.Protocol, 0, len(protocolVersions))
	for _, version := range protocolVersions {
//
		version := version //
		manager.SubProtocols = append(manager.SubProtocols, p2p.Protocol{
			Name:    "les",
			Version: version,
			Length:  ProtocolLengths[version],
			Run: func(p *p2p.Peer, rw p2p.MsgReadWriter) error {
				var entry *poolEntry
				peer := manager.newPeer(int(version), networkId, p, rw)
				if manager.serverPool != nil {
					addr := p.RemoteAddr().(*net.TCPAddr)
					entry = manager.serverPool.connect(peer, addr.IP, uint16(addr.Port))
				}
				peer.poolEntry = entry
				select {
				case manager.newPeerCh <- peer:
					manager.wg.Add(1)
					defer manager.wg.Done()
					err := manager.handle(peer)
					if entry != nil {
						manager.serverPool.disconnect(entry)
					}
					return err
				case <-manager.quitSync:
					if entry != nil {
						manager.serverPool.disconnect(entry)
					}
					return p2p.DiscQuitting
				}
			},
			NodeInfo: func() interface{} {
				return manager.NodeInfo()
			},
			PeerInfo: func(id discover.NodeID) interface{} {
				if p := manager.peers.Peer(fmt.Sprintf("%x", id[:8])); p != nil {
					return p.Info()
				}
				return nil
			},
		})
	}
	if len(manager.SubProtocols) == 0 {
		return nil, errIncompatibleConfig
	}

	removePeer := manager.removePeer
	if disableClientRemovePeer {
		removePeer = func(id string) {}
	}

	if lightSync {
		manager.downloader = downloader.New(downloader.LightSync, chainDb, manager.eventMux, nil, blockchain, removePeer)
		manager.peers.notify((*downloaderPeerNotify)(manager))
		manager.fetcher = newLightFetcher(manager)
	}

	return manager, nil
}

//
func (pm *ProtocolManager) removePeer(id string) {
	pm.peers.Unregister(id)
}

func (pm *ProtocolManager) Start() {
	if pm.lightSync {
		go pm.syncer()
	} else {
		go func() {
			for range pm.newPeerCh {
			}
		}()
	}
}

func (pm *ProtocolManager) Stop() {
//
//
	log.Info("Stopping light Bgmchain protocol")

//
//
	pm.noMorePeers <- struct{}{}

	close(pm.quitSync) //

//
//
//
//
	pm.peers.Close()

//
	pm.wg.Wait()

	log.Info("Light Bgmchain protocol stopped")
}

func (pm *ProtocolManager) newPeer(pv int, nv uint64, p *p2p.Peer, rw p2p.MsgReadWriter) *peer {
	return newPeer(pv, nv, p, newMeteredMsgWriter(rw))
}

//
//
func (pm *ProtocolManager) handle(p *peer) error {
	p.Log().Debug("Light Bgmchain peer connected", "name", p.Name())

//
	td, head, genesis := pm.blockchain.Status()
	headNum := core.GetBlockNumber(pm.chainDb, head)
	if err := p.Handshake(td, head, headNum, genesis, pm.server); err != nil {
		p.Log().Debug("Light Bgmchain handshake failed", "err", err)
		return err
	}
	if rw, ok := p.rw.(*meteredMsgReadWriter); ok {
		rw.Init(p.version)
	}
//
	if err := pm.peers.Register(p); err != nil {
		p.Log().Error("Light Bgmchain peer registration failed", "err", err)
		return err
	}
	defer func() {
		if pm.server != nil && pm.server.fcManager != nil && p.fcClient != nil {
			p.fcClient.Remove(pm.server.fcManager)
		}
		pm.removePeer(p.id)
	}()
//
	if pm.lightSync {
		p.lock.Lock()
		head := p.headInfo
		p.lock.Unlock()
		if pm.fetcher != nil {
			pm.fetcher.announce(p, head)
		}

		if p.poolEntry != nil {
			pm.serverPool.registered(p.poolEntry)
		}
	}

	stop := make(chan struct{})
	defer close(stop)
	go func() {
//
		for {
			select {
			case announce := <-p.announceChn:
				p.SendAnnounce(announce)
			case <-stop:
				return
			}
		}
	}()

//
	for {
		if err := pm.handleMsg(p); err != nil {
			p.Log().Debug("Light Bgmchain message handling failed", "err", err)
			return err
		}
	}
}

var reqList = []uint64{GetBlockHeadersMsg, GetBlockBodiesMsg, GetCodeMsg, GetReceiptsMsg, GetProofsV1Msg, SendTxMsg, SendTxV2Msg, GetTxStatusMsg, GetHeaderProofsMsg, GetProofsV2Msg, GetHelperTrieProofsMsg}

//
//
func (pm *ProtocolManager) handleMsg(p *peer) error {
//
	msg, err := p.rw.ReadMsg()
	if err != nil {
		return err
	}
	p.Log().Trace("Light Bgmchain message arrived", "code", msg.Code, "bytes", msg.Size)

	costs := p.fcCosts[msg.Code]
	reject := func(reqCnt, maxCnt uint64) bool {
		if p.fcClient == nil || reqCnt > maxCnt {
			return true
		}
		bufValue, _ := p.fcClient.AcceptRequest()
		cost := costs.baseCost + reqCnt*costs.reqCost
		if cost > pm.server.defParams.BufLimit {
			cost = pm.server.defParams.BufLimit
		}
		if cost > bufValue {
			recharge := time.Duration((cost - bufValue) * 1000000 / pm.server.defParams.MinRecharge)
			p.Log().Error("Request came too early", "recharge", common.PrettyDuration(recharge))
			return true
		}
		return false
	}

	if msg.Size > ProtocolMaxMsgSize {
		return errResp(ErrMsgTooLarge, "%v > %v", msg.Size, ProtocolMaxMsgSize)
	}
	defer msg.Discard()

	var deliverMsg *Msg

//
	switch msg.Code {
	case StatusMsg:
		p.Log().Trace("Received status message")
//
		return errResp(ErrExtraStatusMsg, "uncontrolled status message")

//
	case AnnounceMsg:
		p.Log().Trace("Received announce message")
		if p.requestAnnounceType == announceTypeNone {
			return errResp(ErrUnexpectedResponse, "")
		}

		var req announceData
		if err := msg.Decode(&req); err != nil {
			return errResp(ErrDecode, "%v: %v", msg, err)
		}

		if p.requestAnnounceType == announceTypeSigned {
			if err := req.checkSignature(p.pubKey); err != nil {
				p.Log().Trace("Invalid announcement signature", "err", err)
				return err
			}
			p.Log().Trace("Valid announcement signature")
		}

		p.Log().Trace("Announce message content", "number", req.Number, "hash", req.Hash, "td", req.Td, "reorg", req.ReorgDepth)
		if pm.fetcher != nil {
			pm.fetcher.announce(p, &req)
		}

	case GetBlockHeadersMsg:
		p.Log().Trace("Received block header request")
//
		var req struct {
			ReqID uint64
			Query getBlockHeadersData
		}
		if err := msg.Decode(&req); err != nil {
			return errResp(ErrDecode, "%v: %v", msg, err)
		}

		query := req.Query
		if reject(query.Amount, MaxHeaderFetch) {
			return errResp(ErrRequestRejected, "")
		}

		hashMode := query.Origin.Hash != (common.Hash{})

//
		var (
			bytes   common.StorageSize
			headers []*types.Header
			unknown bool
		)
		for !unknown && len(headers) < int(query.Amount) && bytes < softResponseLimit {
//
			var origin *types.Header
			if hashMode {
				origin = pm.blockchain.GetHeaderByHash(query.Origin.Hash)
			} else {
				origin = pm.blockchain.GetHeaderByNumber(query.Origin.Number)
			}
			if origin == nil {
				break
			}
			number := origin.Number.Uint64()
			headers = append(headers, origin)
			bytes += estHeaderRlpSize

//
			switch {
			case query.Origin.Hash != (common.Hash{}) && query.Reverse:
//
				for i := 0; i < int(query.Skip)+1; i++ {
					if header := pm.blockchain.GetHeader(query.Origin.Hash, number); header != nil {
						query.Origin.Hash = header.ParentHash
						number--
					} else {
						unknown = true
						break
					}
				}
			case query.Origin.Hash != (common.Hash{}) && !query.Reverse:
//
				if header := pm.blockchain.GetHeaderByNumber(origin.Number.Uint64() + query.Skip + 1); header != nil {
					if pm.blockchain.GetBlockHashesFromHash(header.Hash(), query.Skip+1)[query.Skip] == query.Origin.Hash {
						query.Origin.Hash = header.Hash()
					} else {
						unknown = true
					}
				} else {
					unknown = true
				}
			case query.Reverse:
//
				if query.Origin.Number >= query.Skip+1 {
					query.Origin.Number -= (query.Skip + 1)
				} else {
					unknown = true
				}

			case !query.Reverse:
//
				query.Origin.Number += (query.Skip + 1)
			}
		}

		bv, rcost := p.fcClient.RequestProcessed(costs.baseCost + query.Amount*costs.reqCost)
		pm.server.fcCostStats.update(msg.Code, query.Amount, rcost)
		return p.SendBlockHeaders(req.ReqID, bv, headers)

	case BlockHeadersMsg:
		if pm.downloader == nil {
			return errResp(ErrUnexpectedResponse, "")
		}

		p.Log().Trace("Received block header response message")
//
		var resp struct {
			ReqID, BV uint64
			Headers   []*types.Header
		}
		if err := msg.Decode(&resp); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
		p.fcServer.GotReply(resp.ReqID, resp.BV)
		if pm.fetcher != nil && pm.fetcher.requestedID(resp.ReqID) {
			pm.fetcher.deliverHeaders(p, resp.ReqID, resp.Headers)
		} else {
			err := pm.downloader.DeliverHeaders(p.id, resp.Headers)
			if err != nil {
				log.Debug(fmt.Sprint(err))
			}
		}

	case GetBlockBodiesMsg:
		p.Log().Trace("Received block bodies request")
//
		var req struct {
			ReqID  uint64
			Hashes []common.Hash
		}
		if err := msg.Decode(&req); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
//
		var (
			bytes  int
			bodies []rlp.RawValue
		)
		reqCnt := len(req.Hashes)
		if reject(uint64(reqCnt), MaxBodyFetch) {
			return errResp(ErrRequestRejected, "")
		}
		for _, hash := range req.Hashes {
			if bytes >= softResponseLimit {
				break
			}
//
			if data := core.GetBodyRLP(pm.chainDb, hash, core.GetBlockNumber(pm.chainDb, hash)); len(data) != 0 {
				bodies = append(bodies, data)
				bytes += len(data)
			}
		}
		bv, rcost := p.fcClient.RequestProcessed(costs.baseCost + uint64(reqCnt)*costs.reqCost)
		pm.server.fcCostStats.update(msg.Code, uint64(reqCnt), rcost)
		return p.SendBlockBodiesRLP(req.ReqID, bv, bodies)

	case BlockBodiesMsg:
		if pm.odr == nil {
			return errResp(ErrUnexpectedResponse, "")
		}

		p.Log().Trace("Received block bodies response")
//
		var resp struct {
			ReqID, BV uint64
			Data      []*types.Body
		}
		if err := msg.Decode(&resp); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
		p.fcServer.GotReply(resp.ReqID, resp.BV)
		deliverMsg = &Msg{
			MsgType: MsgBlockBodies,
			ReqID:   resp.ReqID,
			Obj:     resp.Data,
		}

	case GetCodeMsg:
		p.Log().Trace("Received code request")
//
		var req struct {
			ReqID uint64
			Reqs  []CodeReq
		}
		if err := msg.Decode(&req); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
//
		var (
			bytes int
			data  [][]byte
		)
		reqCnt := len(req.Reqs)
		if reject(uint64(reqCnt), MaxCodeFetch) {
			return errResp(ErrRequestRejected, "")
		}
		for _, req := range req.Reqs {
//
			if header := core.GetHeader(pm.chainDb, req.BHash, core.GetBlockNumber(pm.chainDb, req.BHash)); header != nil {
				if trie, _ := trie.New(header.Root, pm.chainDb); trie != nil {
					sdata := trie.Get(req.AccKey)
					var acc state.Account
					if err := rlp.DecodeBytes(sdata, &acc); err == nil {
						entry, _ := pm.chainDb.Get(acc.CodeHash)
						if bytes+len(entry) >= softResponseLimit {
							break
						}
						data = append(data, entry)
						bytes += len(entry)
					}
				}
			}
		}
		bv, rcost := p.fcClient.RequestProcessed(costs.baseCost + uint64(reqCnt)*costs.reqCost)
		pm.server.fcCostStats.update(msg.Code, uint64(reqCnt), rcost)
		return p.SendCode(req.ReqID, bv, data)

	case CodeMsg:
		if pm.odr == nil {
			return errResp(ErrUnexpectedResponse, "")
		}

		p.Log().Trace("Received code response")
//
		var resp struct {
			ReqID, BV uint64
			Data      [][]byte
		}
		if err := msg.Decode(&resp); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
		p.fcServer.GotReply(resp.ReqID, resp.BV)
		deliverMsg = &Msg{
			MsgType: MsgCode,
			ReqID:   resp.ReqID,
			Obj:     resp.Data,
		}

	case GetReceiptsMsg:
		p.Log().Trace("Received receipts request")
//
		var req struct {
			ReqID  uint64
			Hashes []common.Hash
		}
		if err := msg.Decode(&req); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
//
		var (
			bytes    int
			receipts []rlp.RawValue
		)
		reqCnt := len(req.Hashes)
		if reject(uint64(reqCnt), MaxReceiptFetch) {
			return errResp(ErrRequestRejected, "")
		}
		for _, hash := range req.Hashes {
			if bytes >= softResponseLimit {
				break
			}
//
			results := core.GetBlockReceipts(pm.chainDb, hash, core.GetBlockNumber(pm.chainDb, hash))
			if results == nil {
				if header := pm.blockchain.GetHeaderByHash(hash); header == nil || header.ReceiptHash != types.EmptyRootHash {
					continue
				}
			}
//
			if encoded, err := rlp.EncodeToBytes(results); err != nil {
				log.Error("Failed to encode receipt", "err", err)
			} else {
				receipts = append(receipts, encoded)
				bytes += len(encoded)
			}
		}
		bv, rcost := p.fcClient.RequestProcessed(costs.baseCost + uint64(reqCnt)*costs.reqCost)
		pm.server.fcCostStats.update(msg.Code, uint64(reqCnt), rcost)
		return p.SendReceiptsRLP(req.ReqID, bv, receipts)

	case ReceiptsMsg:
		if pm.odr == nil {
			return errResp(ErrUnexpectedResponse, "")
		}

		p.Log().Trace("Received receipts response")
//
		var resp struct {
			ReqID, BV uint64
			Receipts  []types.Receipts
		}
		if err := msg.Decode(&resp); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
		p.fcServer.GotReply(resp.ReqID, resp.BV)
		deliverMsg = &Msg{
			MsgType: MsgReceipts,
			ReqID:   resp.ReqID,
			Obj:     resp.Receipts,
		}

	case GetProofsV1Msg:
		p.Log().Trace("Received proofs request")
//
		var req struct {
			ReqID uint64
			Reqs  []ProofReq
		}
		if err := msg.Decode(&req); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
//
		var (
			bytes  int
			proofs proofsData
		)
		reqCnt := len(req.Reqs)
		if reject(uint64(reqCnt), MaxProofsFetch) {
			return errResp(ErrRequestRejected, "")
		}
		for _, req := range req.Reqs {
			if bytes >= softResponseLimit {
				break
			}
//
			if header := core.GetHeader(pm.chainDb, req.BHash, core.GetBlockNumber(pm.chainDb, req.BHash)); header != nil {
				if tr, _ := trie.New(header.Root, pm.chainDb); tr != nil {
					if len(req.AccKey) > 0 {
						sdata := tr.Get(req.AccKey)
						tr = nil
						var acc state.Account
						if err := rlp.DecodeBytes(sdata, &acc); err == nil {
							tr, _ = trie.New(acc.Root, pm.chainDb)
						}
					}
					if tr != nil {
						var proof light.NodeList
						tr.Prove(req.Key, 0, &proof)
						proofs = append(proofs, proof)
						bytes += proof.DataSize()
					}
				}
			}
		}
		bv, rcost := p.fcClient.RequestProcessed(costs.baseCost + uint64(reqCnt)*costs.reqCost)
		pm.server.fcCostStats.update(msg.Code, uint64(reqCnt), rcost)
		return p.SendProofs(req.ReqID, bv, proofs)

	case GetProofsV2Msg:
		p.Log().Trace("Received les/2 proofs request")
//
		var req struct {
			ReqID uint64
			Reqs  []ProofReq
		}
		if err := msg.Decode(&req); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
//
		var (
			lastBHash  common.Hash
			lastAccKey []byte
			tr, str    *trie.Trie
		)
		reqCnt := len(req.Reqs)
		if reject(uint64(reqCnt), MaxProofsFetch) {
			return errResp(ErrRequestRejected, "")
		}

		nodes := light.NewNodeSet()

		for _, req := range req.Reqs {
			if nodes.DataSize() >= softResponseLimit {
				break
			}
			if tr == nil || req.BHash != lastBHash {
				if header := core.GetHeader(pm.chainDb, req.BHash, core.GetBlockNumber(pm.chainDb, req.BHash)); header != nil {
					tr, _ = trie.New(header.Root, pm.chainDb)
				} else {
					tr = nil
				}
				lastBHash = req.BHash
				str = nil
			}
			if tr != nil {
				if len(req.AccKey) > 0 {
					if str == nil || !bytes.Equal(req.AccKey, lastAccKey) {
						sdata := tr.Get(req.AccKey)
						str = nil
						var acc state.Account
						if err := rlp.DecodeBytes(sdata, &acc); err == nil {
							str, _ = trie.New(acc.Root, pm.chainDb)
						}
						lastAccKey = common.CopyBytes(req.AccKey)
					}
					if str != nil {
						str.Prove(req.Key, req.FromLevel, nodes)
					}
				} else {
					tr.Prove(req.Key, req.FromLevel, nodes)
				}
			}
		}
		proofs := nodes.NodeList()
		bv, rcost := p.fcClient.RequestProcessed(costs.baseCost + uint64(reqCnt)*costs.reqCost)
		pm.server.fcCostStats.update(msg.Code, uint64(reqCnt), rcost)
		return p.SendProofsV2(req.ReqID, bv, proofs)

	case ProofsV1Msg:
		if pm.odr == nil {
			return errResp(ErrUnexpectedResponse, "")
		}

		p.Log().Trace("Received proofs response")
//
		var resp struct {
			ReqID, BV uint64
			Data      []light.NodeList
		}
		if err := msg.Decode(&resp); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
		p.fcServer.GotReply(resp.ReqID, resp.BV)
		deliverMsg = &Msg{
			MsgType: MsgProofsV1,
			ReqID:   resp.ReqID,
			Obj:     resp.Data,
		}

	case ProofsV2Msg:
		if pm.odr == nil {
			return errResp(ErrUnexpectedResponse, "")
		}

		p.Log().Trace("Received les/2 proofs response")
//
		var resp struct {
			ReqID, BV uint64
			Data      light.NodeList
		}
		if err := msg.Decode(&resp); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
		p.fcServer.GotReply(resp.ReqID, resp.BV)
		deliverMsg = &Msg{
			MsgType: MsgProofsV2,
			ReqID:   resp.ReqID,
			Obj:     resp.Data,
		}

	case GetHeaderProofsMsg:
		p.Log().Trace("Received headers proof request")
//
		var req struct {
			ReqID uint64
			Reqs  []ChtReq
		}
		if err := msg.Decode(&req); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
//
		var (
			bytes  int
			proofs []ChtResp
		)
		reqCnt := len(req.Reqs)
		if reject(uint64(reqCnt), MaxHelperTrieProofsFetch) {
			return errResp(ErrRequestRejected, "")
		}
		trieDb := bgmdb.NewTable(pm.chainDb, light.ChtTablePrefix)
		for _, req := range req.Reqs {
			if bytes >= softResponseLimit {
				break
			}

			if header := pm.blockchain.GetHeaderByNumber(req.BlockNum); header != nil {
				sectionHead := core.GetCanonicalHash(pm.chainDb, (req.ChtNum+1)*light.ChtV1Frequency-1)
				if root := light.GetChtRoot(pm.chainDb, req.ChtNum, sectionHead); root != (common.Hash{}) {
					if tr, _ := trie.New(root, trieDb); tr != nil {
						var encNumber [8]byte
						binary.BigEndian.PutUint64(encNumber[:], req.BlockNum)
						var proof light.NodeList
						tr.Prove(encNumber[:], 0, &proof)
						proofs = append(proofs, ChtResp{Header: header, Proof: proof})
						bytes += proof.DataSize() + estHeaderRlpSize
					}
				}
			}
		}
		bv, rcost := p.fcClient.RequestProcessed(costs.baseCost + uint64(reqCnt)*costs.reqCost)
		pm.server.fcCostStats.update(msg.Code, uint64(reqCnt), rcost)
		return p.SendHeaderProofs(req.ReqID, bv, proofs)

	case GetHelperTrieProofsMsg:
		p.Log().Trace("Received helper trie proof request")
//
		var req struct {
			ReqID uint64
			Reqs  []HelperTrieReq
		}
		if err := msg.Decode(&req); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
//
		var (
			auxBytes int
			auxData  [][]byte
		)
		reqCnt := len(req.Reqs)
		if reject(uint64(reqCnt), MaxHelperTrieProofsFetch) {
			return errResp(ErrRequestRejected, "")
		}

		var (
			lastIdx  uint64
			lastType uint
			root     common.Hash
			tr       *trie.Trie
		)

		nodes := light.NewNodeSet()

		for _, req := range req.Reqs {
			if nodes.DataSize()+auxBytes >= softResponseLimit {
				break
			}
			if tr == nil || req.HelperTrieType != lastType || req.TrieIdx != lastIdx {
				var prefix string
				root, prefix = pm.getHelperTrie(req.HelperTrieType, req.TrieIdx)
				if root != (common.Hash{}) {
					if t, err := trie.New(root, bgmdb.NewTable(pm.chainDb, prefix)); err == nil {
						tr = t
					}
				}
				lastType = req.HelperTrieType
				lastIdx = req.TrieIdx
			}
			if req.AuxReq == auxRoot {
				var data []byte
				if root != (common.Hash{}) {
					data = root[:]
				}
				auxData = append(auxData, data)
				auxBytes += len(data)
			} else {
				if tr != nil {
					tr.Prove(req.Key, req.FromLevel, nodes)
				}
				if req.AuxReq != 0 {
					data := pm.getHelperTrieAuxData(req)
					auxData = append(auxData, data)
					auxBytes += len(data)
				}
			}
		}
		proofs := nodes.NodeList()
		bv, rcost := p.fcClient.RequestProcessed(costs.baseCost + uint64(reqCnt)*costs.reqCost)
		pm.server.fcCostStats.update(msg.Code, uint64(reqCnt), rcost)
		return p.SendHelperTrieProofs(req.ReqID, bv, HelperTrieResps{Proofs: proofs, AuxData: auxData})

	case HeaderProofsMsg:
		if pm.odr == nil {
			return errResp(ErrUnexpectedResponse, "")
		}

		p.Log().Trace("Received headers proof response")
		var resp struct {
			ReqID, BV uint64
			Data      []ChtResp
		}
		if err := msg.Decode(&resp); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
		p.fcServer.GotReply(resp.ReqID, resp.BV)
		deliverMsg = &Msg{
			MsgType: MsgHeaderProofs,
			ReqID:   resp.ReqID,
			Obj:     resp.Data,
		}

	case HelperTrieProofsMsg:
		if pm.odr == nil {
			return errResp(ErrUnexpectedResponse, "")
		}

		p.Log().Trace("Received helper trie proof response")
		var resp struct {
			ReqID, BV uint64
			Data      HelperTrieResps
		}
		if err := msg.Decode(&resp); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}

		p.fcServer.GotReply(resp.ReqID, resp.BV)
		deliverMsg = &Msg{
			MsgType: MsgHelperTrieProofs,
			ReqID:   resp.ReqID,
			Obj:     resp.Data,
		}

	case SendTxMsg:
		if pm.txpool == nil {
			return errResp(ErrRequestRejected, "")
		}
//
		var txs []*types.Transaction
		if err := msg.Decode(&txs); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
		reqCnt := len(txs)
		if reject(uint64(reqCnt), MaxTxSend) {
			return errResp(ErrRequestRejected, "")
		}
		pm.txpool.AddRemotes(txs)

		_, rcost := p.fcClient.RequestProcessed(costs.baseCost + uint64(reqCnt)*costs.reqCost)
		pm.server.fcCostStats.update(msg.Code, uint64(reqCnt), rcost)

	case SendTxV2Msg:
		if pm.txpool == nil {
			return errResp(ErrRequestRejected, "")
		}
//
		var req struct {
			ReqID uint64
			Txs   []*types.Transaction
		}
		if err := msg.Decode(&req); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
		reqCnt := len(req.Txs)
		if reject(uint64(reqCnt), MaxTxSend) {
			return errResp(ErrRequestRejected, "")
		}

		hashes := make([]common.Hash, len(req.Txs))
		for i, tx := range req.Txs {
			hashes[i] = tx.Hash()
		}
		stats := pm.txStatus(hashes)
		for i, stat := range stats {
			if stat.Status == core.TxStatusUnknown {
				if errs := pm.txpool.AddRemotes([]*types.Transaction{req.Txs[i]}); errs[0] != nil {
					stats[i].Error = errs[0]
					continue
				}
				stats[i] = pm.txStatus([]common.Hash{hashes[i]})[0]
			}
		}

		bv, rcost := p.fcClient.RequestProcessed(costs.baseCost + uint64(reqCnt)*costs.reqCost)
		pm.server.fcCostStats.update(msg.Code, uint64(reqCnt), rcost)

		return p.SendTxStatus(req.ReqID, bv, stats)

	case GetTxStatusMsg:
		if pm.txpool == nil {
			return errResp(ErrUnexpectedResponse, "")
		}
//
		var req struct {
			ReqID  uint64
			Hashes []common.Hash
		}
		if err := msg.Decode(&req); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}
		reqCnt := len(req.Hashes)
		if reject(uint64(reqCnt), MaxTxStatus) {
			return errResp(ErrRequestRejected, "")
		}
		bv, rcost := p.fcClient.RequestProcessed(costs.baseCost + uint64(reqCnt)*costs.reqCost)
		pm.server.fcCostStats.update(msg.Code, uint64(reqCnt), rcost)

		return p.SendTxStatus(req.ReqID, bv, pm.txStatus(req.Hashes))

	case TxStatusMsg:
		if pm.odr == nil {
			return errResp(ErrUnexpectedResponse, "")
		}

		p.Log().Trace("Received tx status response")
		var resp struct {
			ReqID, BV uint64
			Status    []core.TxStatus
		}
		if err := msg.Decode(&resp); err != nil {
			return errResp(ErrDecode, "msg %v: %v", msg, err)
		}

		p.fcServer.GotReply(resp.ReqID, resp.BV)

	default:
		p.Log().Trace("Received unknown message", "code", msg.Code)
		return errResp(ErrInvalidMsgCode, "%v", msg.Code)
	}

	if deliverMsg != nil {
		err := pm.retriever.deliver(p, deliverMsg)
		if err != nil {
			p.responseErrors++
			if p.responseErrors > maxResponseErrors {
				return err
			}
		}
	}
	return nil
}

//
func (pm *ProtocolManager) getHelperTrie(id uint, idx uint64) (common.Hash, string) {
	switch id {
	case htCanonical:
		sectionHead := core.GetCanonicalHash(pm.chainDb, (idx+1)*light.ChtFrequency-1)
		return light.GetChtV2Root(pm.chainDb, idx, sectionHead), light.ChtTablePrefix
	case htBloomBits:
		sectionHead := core.GetCanonicalHash(pm.chainDb, (idx+1)*light.BloomTrieFrequency-1)
		return light.GetBloomTrieRoot(pm.chainDb, idx, sectionHead), light.BloomTrieTablePrefix
	}
	return common.Hash{}, ""
}

//
func (pm *ProtocolManager) getHelperTrieAuxData(req HelperTrieReq) []byte {
	if req.HelperTrieType == htCanonical && req.AuxReq == auxHeader {
		if len(req.Key) != 8 {
			return nil
		}
		blockNum := binary.BigEndian.Uint64(req.Key)
		hash := core.GetCanonicalHash(pm.chainDb, blockNum)
		return core.GetHeaderRLP(pm.chainDb, hash, blockNum)
	}
	return nil
}

func (pm *ProtocolManager) txStatus(hashes []common.Hash) []txStatus {
	stats := make([]txStatus, len(hashes))
	for i, stat := range pm.txpool.Status(hashes) {
//
		stats[i].Status = stat

//
		if stat == core.TxStatusUnknown {
			if block, number, index := core.GetTxLookupEntry(pm.chainDb, hashes[i]); block != (common.Hash{}) {
				stats[i].Status = core.TxStatusIncluded
				stats[i].Lookup = &core.TxLookupEntry{BlockHash: block, BlockIndex: number, Index: index}
			}
		}
	}
	return stats
}

//
func (self *ProtocolManager) NodeInfo() *bgm.BgmNodeInfo {
	return &bgm.BgmNodeInfo{
		Network:    self.networkId,
		Difficulty: self.blockchain.GetTdByHash(self.blockchain.LastBlockHash()),
		Genesis:    self.blockchain.Genesis().Hash(),
		Head:       self.blockchain.LastBlockHash(),
	}
}

//
type downloaderPeerNotify ProtocolManager

type peerConnection struct {
	manager *ProtocolManager
	peer    *peer
}

func (pc *peerConnection) Head() (common.Hash, *big.Int) {
	return pc.peer.HeadAndTd()
}

func (pc *peerConnection) RequestHeadersByHash(origin common.Hash, amount int, skip int, reverse bool) error {
	reqID := genReqID()
	rq := &distReq{
		getCost: func(dp distPeer) uint64 {
			peer := dp.(*peer)
			return peer.GetRequestCost(GetBlockHeadersMsg, amount)
		},
		canSend: func(dp distPeer) bool {
			return dp.(*peer) == pc.peer
		},
		request: func(dp distPeer) func() {
			peer := dp.(*peer)
			cost := peer.GetRequestCost(GetBlockHeadersMsg, amount)
			peer.fcServer.QueueRequest(reqID, cost)
			return func() { peer.RequestHeadersByHash(reqID, cost, origin, amount, skip, reverse) }
		},
	}
	_, ok := <-pc.manager.reqDist.queue(rq)
	if !ok {
		return ErrNoPeers
	}
	return nil
}

func (pc *peerConnection) RequestHeadersByNumber(origin uint64, amount int, skip int, reverse bool) error {
	reqID := genReqID()
	rq := &distReq{
		getCost: func(dp distPeer) uint64 {
			peer := dp.(*peer)
			return peer.GetRequestCost(GetBlockHeadersMsg, amount)
		},
		canSend: func(dp distPeer) bool {
			return dp.(*peer) == pc.peer
		},
		request: func(dp distPeer) func() {
			peer := dp.(*peer)
			cost := peer.GetRequestCost(GetBlockHeadersMsg, amount)
			peer.fcServer.QueueRequest(reqID, cost)
			return func() { peer.RequestHeadersByNumber(reqID, cost, origin, amount, skip, reverse) }
		},
	}
	_, ok := <-pc.manager.reqDist.queue(rq)
	if !ok {
		return ErrNoPeers
	}
	return nil
}

func (d *downloaderPeerNotify) registerPeer(p *peer) {
	pm := (*ProtocolManager)(d)
	pc := &peerConnection{
		manager: pm,
		peer:    p,
	}
	pm.downloader.RegisterLightPeer(p.id, bgmVersion, pc)
}

func (d *downloaderPeerNotify) unregisterPeer(p *peer) {
	pm := (*ProtocolManager)(d)
	pm.downloader.UnregisterPeer(p.id)
}
