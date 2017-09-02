package raft

//
// this is an outline of the API that raft must expose to
// the service (or tester). see comments below for
// each of these functions for more details.
//
// rf = Make(...)
//   create a new Raft server.
// rf.Start(command interface{}) (index, term, isleader)
//   start agreement on a new log entry
// rf.GetState() (term, isLeader)
//   ask a Raft for its current term, and whether it thinks it is leader
// ApplyMsg
//   each time a new entry is committed to the log, each Raft peer
//   should send an ApplyMsg to the service (or tester)
//   in the same server.
//

import (
	"fmt"
	"labrpc"
	"math/rand"
	"sync"
	"time"

	"sync/atomic"
)

// import "bytes"
// import "encoding/gob"

const (
	FOLLOWER = iota
	CANDIDATE
	LEADER

	HEARTBEAT_INTERVAL    = 100
	MIN_ELECTION_INTERVAL = 400
	MAX_ELECTION_INTERVAL = 500
)

//
// as each Raft peer becomes aware that successive log entries are
// committed, the peer should send an ApplyMsg to the service (or
// tester) on the same server, via the applyCh passed to Make().
//
type ApplyMsg struct {
	Index       int
	Command     interface{}
	UseSnapshot bool   // ignore for lab2; only used in lab3
	Snapshot    []byte // ignore for lab2; only used in lab3
}

type LogEntry struct {
	Term    int
	Index   int
	Command interface{}
}

//
// A Go object implementing a single Raft peer.
//
type Raft struct {
	mu        sync.Mutex          // Lock to protect shared access to this peer's state
	peers     []*labrpc.ClientEnd // RPC end points of all peers
	persister *Persister          // Object to hold this peer's persisted state
	me        int                 // this peer's index into peers[]

	// Your data here (2A, 2B, 2C).
	// Look at the paper's Figure 2 for a description of what
	// state a Raft server must maintain.
	votedFor     int
	voteAcquired int
	state        int32
	currentTerm  int32

	electionTimer *time.Timer
	voteCh        chan struct{} // 成功投票的信号
	appendCh      chan struct{} // 成功更新 log 的信号

	// Part B
	log         []LogEntry
	commitIndex int
	lastApplied int
	nextIndex   []int
	matchIndex  []int
	applyCh     chan ApplyMsg
}

// atomic operations
func (rf *Raft) getTerm() int32 {
	return atomic.LoadInt32(&rf.currentTerm)
}

func (rf *Raft) incrementTerm() {
	atomic.AddInt32(&rf.currentTerm, 1)
}

func (rf *Raft) isState(state int32) bool {
	return atomic.LoadInt32(&rf.state) == state
}

// return currentTerm and whether this server
// believes it is the leader.
func (rf *Raft) GetState() (int, bool) {

	var term int
	var isleader bool
	// Your code here (2A).
	term = int(rf.getTerm())
	isleader = rf.isState(LEADER)
	return term, isleader
}

//
// save Raft's persistent state to stable storage,
// where it can later be retrieved after a crash and restart.
// see paper's Figure 2 for a description of what should be persistent.
//
func (rf *Raft) persist() {
	// Your code here (2C).
	// Example:
	// w := new(bytes.Buffer)
	// e := gob.NewEncoder(w)
	// e.Encode(rf.xxx)
	// e.Encode(rf.yyy)
	// data := w.Bytes()
	// rf.persister.SaveRaftState(data)
}

//
// restore previously persisted state.
//
func (rf *Raft) readPersist(data []byte) {
	// Your code here (2C).
	// Example:
	// r := bytes.NewBuffer(data)
	// d := gob.NewDecoder(r)
	// d.Decode(&rf.xxx)
	// d.Decode(&rf.yyy)
	if data == nil || len(data) < 1 { // bootstrap without any state?
		return
	}
}

//
// example RequestVote RPC arguments structure.
// field names must start with capital letters!
//
type RequestVoteArgs struct {
	// Your data here (2A, 2B).
	Term        int32
	CandidateId int
	// Part B
	LastLogIndex int
	LastLogTerm  int
}

//
// example RequestVote RPC reply structure.
// field names must start with capital letters!
//
type RequestVoteReply struct {
	// Your data here (2A).
	Term        int32
	VoteGranted bool
}

//
// example RequestVote RPC handler.
//
func (rf *Raft) RequestVote(args *RequestVoteArgs, reply *RequestVoteReply) {
	// Your code here (2A, 2B).
	rf.mu.Lock()
	defer rf.mu.Unlock()
	if args.Term < rf.currentTerm {
		reply.VoteGranted = false
		reply.Term = rf.currentTerm
		return
	} else if args.Term > rf.currentTerm {
		rf.currentTerm = args.Term
		rf.updateStateTo(FOLLOWER)
		rf.votedFor = args.CandidateId
		reply.VoteGranted = true
	} else {
		if rf.votedFor == -1 {
			rf.votedFor = args.CandidateId
			reply.VoteGranted = true
		} else {
			reply.VoteGranted = false
		}
	}
	// Part B
	// check log
	this_lastLogTerm := rf.log[rf.getLastLogIndex()].Term
	if this_lastLogTerm > args.LastLogTerm {
		reply.VoteGranted = false
	} else if this_lastLogTerm == args.LastLogTerm {
		if rf.getLastLogIndex() > args.LastLogIndex {
			reply.VoteGranted = false
		}
	}
	if reply.VoteGranted == true {
		go func() { rf.voteCh <- struct{}{} }()
	}
}

type AppendEntriesArgs struct {
	Term     int32
	LeaderId int

	// Part B
	PrevLogIndex int
	PrevLogTerm  int
	Entries      []LogEntry
	LeaderCommit int
}

type AppendEntriesReply struct {
	Term    int32
	Success bool
	// optimization
	NextTrial int
}

func (rf *Raft) AppendEntries(args *AppendEntriesArgs, reply *AppendEntriesReply) {
	inform := func() {
		go func() { rf.appendCh <- struct{}{} }()
	}
	rf.mu.Lock()
	defer inform()
	defer rf.mu.Unlock()

	// term and return value
	if args.Term < rf.currentTerm {
		reply.Success = false
		reply.Term = rf.currentTerm
		return
	} else if args.Term > rf.currentTerm {
		reply.Success = true
		rf.currentTerm = args.Term
		rf.updateStateTo(FOLLOWER)
	} else {
		reply.Success = true
	}

	// log
	if args.PrevLogIndex > rf.getLastLogIndex() {
		reply.Success = false
		// optimization
		reply.NextTrial = rf.getLastLogIndex() + 1
		return
	}

	if args.PrevLogTerm != rf.log[args.PrevLogIndex].Term {
		reply.Success = false
		// optimization
		badTerm := rf.log[args.PrevLogIndex].Term
		i := args.PrevLogIndex
		for ; rf.log[i].Term == badTerm; i-- {
		}
		reply.NextTrial = i + 1
		return
	}

	conflictIdx := -1
	if rf.getLastLogIndex() < args.PrevLogIndex+len(args.Entries) {
		// 长度不够，肯定不匹配
		conflictIdx = args.PrevLogIndex + 1
	} else {
		for idx := 0; idx < len(args.Entries); idx++ {
			if rf.log[idx+args.PrevLogIndex+1].Term != args.Entries[idx].Term {
				conflictIdx = idx + args.PrevLogIndex + 1
				break
			}
		}
	}
	if conflictIdx != -1 {
		rf.log = append(rf.log[:args.PrevLogIndex+1], args.Entries...)
	}

	// commit
	if args.LeaderCommit > rf.commitIndex {
		if args.LeaderCommit < rf.getLastLogIndex() {
			rf.commitIndex = rf.getLastLogIndex()
		} else {
			rf.commitIndex = args.LeaderCommit
		}
	}
}

//
// example code to send a RequestVote RPC to a server.
// server is the index of the target server in rf.peers[].
// expects RPC arguments in args.
// fills in *reply with RPC reply, so caller should
// pass &reply.
// the types of the args and reply passed to Call() must be
// the same as the types of the arguments declared in the
// handler function (including whether they are pointers).
//
// The labrpc package simulates a lossy network, in which servers
// may be unreachable, and in which requests and replies may be lost.
// Call() sends a request and waits for a reply. If a reply arrives
// within a timeout interval, Call() returns true; otherwise
// Call() returns false. Thus Call() may not return for a while.
// A false return can be caused by a dead server, a live server that
// can't be reached, a lost request, or a lost reply.
//
// Call() is guaranteed to return (perhaps after a delay) *except* if the
// handler function on the server side does not return.  Thus there
// is no need to implement your own timeouts around Call().
//
// look at the comments in ../labrpc/labrpc.go for more details.
//
// if you're having trouble getting RPC to work, check that you've
// capitalized all field names in structs passed over RPC, and
// that the caller passes the address of the reply struct with &, not
// the struct itself.
//
func (rf *Raft) sendRequestVote(server int, args *RequestVoteArgs, reply *RequestVoteReply) bool {
	ok := rf.peers[server].Call("Raft.RequestVote", args, reply)
	return ok
}
func (rf *Raft) sendAppendEntries(server int, args *AppendEntriesArgs, reply *AppendEntriesReply) bool {
	ok := rf.peers[server].Call("Raft.AppendEntries", args, reply)
	return ok
}

//
// the service using Raft (e.g. a k/v server) wants to start
// agreement on the next command to be appended to Raft's log. if this
// server isn't the leader, returns false. otherwise start the
// agreement and return immediately. there is no guarantee that this
// command will ever be committed to the Raft log, since the leader
// may fail or lose an election.
//
// the first return value is the index that the command will appear at
// if it's ever committed. the second return value is the current
// term. the third return value is true if this server believes it is
// the leader.
//
func (rf *Raft) Start(command interface{}) (int, int, bool) {
	index := -1
	term := -1
	isLeader := true

	// Your code here (2B).
	term, isLeader = rf.GetState()
	if isLeader == true {
		rf.mu.Lock()
		index = len(rf.log)
		rf.log = append(rf.log, LogEntry{term, index, command})
		rf.mu.Unlock()
	}

	return index, term, isLeader
}

//
// the tester calls Kill() when a Raft instance won't
// be needed again. you are not required to do anything
// in Kill(), but it might be convenient to (for example)
// turn off debug output from this instance.
//
func (rf *Raft) Kill() {
	// Your code here, if desired.
}

//
// the service or tester wants to create a Raft server. the ports
// of all the Raft servers (including this one) are in peers[]. this
// server's port is peers[me]. all the servers' peers[] arrays
// have the same order. persister is a place for this server to
// save its persistent state, and also initially holds the most
// recent saved state, if any. applyCh is a channel on which the
// tester or service expects Raft to send ApplyMsg messages.
// Make() must return quickly, so it should start goroutines
// for any long-running work.
//
func Make(peers []*labrpc.ClientEnd, me int,
	persister *Persister, applyCh chan ApplyMsg) *Raft {
	rf := &Raft{}
	rf.peers = peers
	rf.persister = persister
	rf.me = me

	// Your initialization code here (2A, 2B, 2C).
	rf.state = FOLLOWER
	rf.votedFor = -1 // init with -1, because server id start from 0
	rf.voteCh = make(chan struct{})
	rf.appendCh = make(chan struct{})

	// Part B
	rf.nextIndex = make([]int, len(rf.peers))
	rf.matchIndex = make([]int, len(rf.peers))
	rf.applyCh = applyCh
	rf.log = make([]LogEntry, 1)

	// initialize from state persisted before a crash
	rf.readPersist(persister.ReadRaftState())

	go rf.startLoop()

	return rf
}

func randElectionDuration() time.Duration {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	return time.Millisecond * time.Duration(r.Int63n(MAX_ELECTION_INTERVAL-MIN_ELECTION_INTERVAL)+MIN_ELECTION_INTERVAL)
}

func (rf *Raft) updateStateTo(state int32) {
	// should always been protected by lock

	if rf.isState(state) {
		return
	}
	stateDesc := []string{"FOLLOWER", "CANDIDATE", "LEADER"}
	preState := rf.state

	switch state {
	case FOLLOWER:
		rf.state = FOLLOWER
		rf.votedFor = -1 // prepare for next election
	case CANDIDATE:
		rf.state = CANDIDATE
		rf.startElection()
	case LEADER:
		for i, _ := range rf.peers {
			rf.nextIndex[i] = rf.getLastLogIndex() + 1
			rf.matchIndex[i] = 0
		}
		rf.state = LEADER
	default:
		fmt.Printf("Warning: invalid state %d, do nothing.\n", state)
	}
	fmt.Printf("In term %d: Server %d transfer from %s to %s\n",
		rf.currentTerm, rf.me, stateDesc[preState], stateDesc[rf.state])
}

func (rf *Raft) startElection() {
	// should always been protected by lock

	rf.incrementTerm()
	rf.votedFor = rf.me
	rf.voteAcquired = 1
	rf.electionTimer.Reset(randElectionDuration())
	rf.broadcastVoteReq()
}

func (rf *Raft) broadcastVoteReq() {
	// has been locked out side in startElection
	var args RequestVoteArgs
	args.Term = rf.currentTerm
	args.CandidateId = rf.me
	args.LastLogIndex = rf.getLastLogIndex()
	args.LastLogTerm = rf.log[args.LastLogIndex].Term
	for i, _ := range rf.peers {
		if i == rf.me {
			continue
		}
		go func(server int) {
			var reply RequestVoteReply
			if rf.isState(CANDIDATE) && rf.sendRequestVote(server, &args, &reply) {
				rf.mu.Lock()
				defer rf.mu.Unlock()

				if reply.VoteGranted == true {
					rf.voteAcquired += 1
				} else {
					if reply.Term > rf.currentTerm {
						rf.currentTerm = reply.Term
						rf.updateStateTo(FOLLOWER)
					}
				}
			} else {
				fmt.Printf("Server %d send vote req failed.\n", rf.me)
			}
		}(i)
	}
}

func (rf *Raft) broadcastAppendEntries() {
	sendAppendEntriesTo := func(server int) bool {
		// 该函数返回 true 代表需要重试，否则返回false
		var args AppendEntriesArgs
		rf.mu.Lock()
		args.Term = rf.currentTerm
		args.LeaderId = rf.me
		args.LeaderCommit = rf.commitIndex
		args.PrevLogIndex = rf.nextIndex[server] - 1
		args.PrevLogTerm = rf.log[args.PrevLogIndex].Term
		if rf.getLastLogIndex() >= rf.nextIndex[server] {
			args.Entries = rf.log[rf.nextIndex[server]:]
		}
		rf.mu.Unlock()

		var reply AppendEntriesReply

		// 发送是并行的
		if rf.isState(LEADER) && rf.sendAppendEntries(server, &args, &reply) {
			rf.mu.Lock()
			defer rf.mu.Unlock()
			if reply.Success == true {
				rf.nextIndex[server] += len(args.Entries)
				rf.matchIndex[server] = rf.nextIndex[server] - 1
			} else {
				// ****** 易错点 ******
				// 由于并行发送，可能收到多个回复
				// 如果已经在之前的回复中失去了 LEADER 身份
				// 则直接不处理其他回复了
				if rf.state != LEADER {
					return false
				}

				if reply.Term > rf.currentTerm {
					// term 不匹配
					rf.currentTerm = reply.Term
					rf.updateStateTo(FOLLOWER)
				} else {
					// log 不匹配
					rf.nextIndex[server] = reply.NextTrial
					return true
				}
			}
		}
		return false
	}
	for i, _ := range rf.peers {
		if i == rf.me {
			continue
		}
		go func(server int) {
			for {
				if sendAppendEntriesTo(server) == false {
					break
				}
			}
		}(i)
	}
}

func (rf *Raft) getLastLogIndex() int {
	return len(rf.log) - 1
}

func (rf *Raft) startLoop() {
	rf.electionTimer = time.NewTimer(randElectionDuration())
	for {
		switch atomic.LoadInt32(&rf.state) {
		case FOLLOWER:
			select {
			case <-rf.voteCh:
				rf.electionTimer.Reset(randElectionDuration())
			case <-rf.appendCh:
				rf.electionTimer.Reset(randElectionDuration())
			case <-rf.electionTimer.C:
				rf.mu.Lock()
				rf.updateStateTo(CANDIDATE)
				rf.mu.Unlock()
			}

		case CANDIDATE:
			rf.mu.Lock()
			select {
			// a candicate will not trigger voteCh
			// because it has voted for itself
			case <-rf.appendCh:
				rf.updateStateTo(FOLLOWER)
			case <-rf.electionTimer.C:
				rf.electionTimer.Reset(randElectionDuration())
				rf.startElection()
			default:
				// check if it has collected enough vote
				if rf.voteAcquired > len(rf.peers)/2 {
					rf.updateStateTo(LEADER)
				}
			}
			rf.mu.Unlock()
		case LEADER:
			rf.broadcastAppendEntries()
			rf.updateCommitIndex()
			time.Sleep(HEARTBEAT_INTERVAL * time.Millisecond)
		}
		go rf.applyLog()
	}
}

func (rf *Raft) updateCommitIndex() {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	for i := rf.getLastLogIndex(); i > rf.commitIndex; i-- {
		// ******* 易错点 *******
		// leader 自身一票
		// 由于 leader 不更新自身的 matchIndex
		// 所以需要特殊处理
		matchedCount := 1
		for i, matched := range rf.matchIndex {
			if i == rf.me {
				continue
			}
			if matched > rf.commitIndex {
				matchedCount += 1
			}
		}
		if matchedCount > len(rf.peers)/2 {
			rf.commitIndex = i
			break
		}
	}
}

func (rf *Raft) applyLog() {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	if rf.commitIndex > rf.lastApplied {
		for i := rf.lastApplied + 1; i <= rf.commitIndex; i++ {
			var msg ApplyMsg
			msg.Index = i
			msg.Command = rf.log[i].Command
			rf.applyCh <- msg
		}
	}
}
