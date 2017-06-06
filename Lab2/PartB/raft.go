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

import "sync"
import "labrpc"

// import "bytes"
// import "encoding/gob"

import (
	"bytes"
	"fmt"
	"math/rand"
	"time"
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

const (
	FOLLOWER = iota
	CANDIDATE
	LEADER

	HEARTBEAT_INTERVAL    = 100 * time.Millisecond
	MIN_ELECTION_INTERVAL = 400
	MAX_ELECTION_INTERVAL = 500
)

type LogEntry struct {
	Index   int // Start from 1
	Term    int
	Command interface{} // Same as the input in Start()
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

	// persistent state
	currentTerm int // 当前任期
	votedFor    int // 投票给了谁

	state int // 目前自己是 FOLLOWER, CANDIDATE 还是 LEADER

	electionTimer *time.Timer // timer

	appendCh chan bool
	voteCh   chan bool

	voteCount int // rf.me 收到的选票

	// Part B
	log         []LogEntry
	commitIndex int
	lastApplied int
	nextIndex   []int
	matchIndex  []int
	applyCh     chan ApplyMsg
}

// return currentTerm and whether this server
// believes it is the leader.
func (rf *Raft) GetState() (int, bool) {

	var term int
	var isleader bool
	// Your code here (2A).
	term = rf.currentTerm
	if rf.state == LEADER {
		isleader = true
	}
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

/***************
 * RequestVote *
 ***************/

//
// example RequestVote RPC arguments structure.
// field names must start with capital letters!
//
type RequestVoteArgs struct {
	// Your data here (2A, 2B).
	Term        int
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
	Term        int
	VoteGranted bool // 收到了对CandidateId的投票
}

//
// example RequestVote RPC handler.
//
func (rf *Raft) RequestVote(args *RequestVoteArgs, reply *RequestVoteReply) {
	// Your code here (2A, 2B).

	if args.Term < rf.currentTerm {
		reply.VoteGranted = false
		reply.Term = rf.currentTerm
		return
	}

	if args.Term > rf.currentTerm {
		rf.currentTerm = args.Term
		rf.updateStateTo(FOLLOWER)
		// if it's a new term, just vote for the first candidate
		rf.votedFor = args.CandidateId
	}
	reply.Term = rf.currentTerm

	// add for part B
	var upToDate bool
	followerLastTerm := rf.log[rf.lastLogIndex()].Term
	if args.LastLogTerm > followerLastTerm {
		upToDate = true
	} else if args.LastLogTerm == followerLastTerm && args.LastLogIndex >= rf.lastLogIndex() {
		upToDate = true
	} else {
		upToDate = false
		DPrintf("Candidate %d's log is out of date: %d(Term %d), follower %d has %d(Term %d)\n",
			args.CandidateId, args.LastLogIndex, args.LastLogTerm, rf.me, rf.lastLogIndex(), followerLastTerm)
	}

	if rf.votedFor == -1 {
		rf.votedFor = args.CandidateId
	}
	if rf.votedFor == args.CandidateId && upToDate {
		// first come first served
		reply.VoteGranted = true
		rf.voteCh <- true
	} else {
		reply.VoteGranted = false
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

/*****************
 * AppendEntries *
 *****************/

type AppendEntriesArgs struct {
	Term     int
	LeaderId int
	// Part B
	PrevLogIndex int
	PrevLogTerm  int
	Entries      []LogEntry
	LeaderCommit int
}

type AppendEntriesReply struct {
	Term    int
	Success bool
}

func (rf *Raft) AppendEntries(args *AppendEntriesArgs, reply *AppendEntriesReply) {
	if args.Term < rf.currentTerm {
		DPrintf("Leader %d outdate to follower %d, (Term %d < %d)...\n", args.LeaderId, rf.me, args.Term, rf.currentTerm)
		reply.Success = false
		reply.Term = rf.currentTerm
		return
	}

	if args.Term > rf.currentTerm {
		rf.currentTerm = args.Term
		rf.updateStateTo(FOLLOWER)
	}
	reply.Term = rf.currentTerm

	if rf.lastLogIndex() < args.PrevLogIndex || rf.log[args.PrevLogIndex].Term != args.PrevLogTerm {
		DPrintf("Follower %d: log unmatched, require Index %d in Term %d, mine is %d\n",
			rf.me, args.PrevLogIndex, args.PrevLogTerm,
			func() int {
				if rf.lastLogIndex() < args.PrevLogIndex {
					return -1
				}
				return rf.log[args.PrevLogIndex].Term
			}())
		reply.Success = false
		// 既然发送者仍然是合格的 Leader, 我们是否需要当作心跳包处理?

		rf.appendCh <- true

		return
	}
	reply.Success = true

	// log replication
	// Lock is expensive, so if there is no Entries, just skip
	if args.Entries != nil {
		var conflict bool
		rf.mu.Lock()
		if rf.lastLogIndex() < args.PrevLogIndex+len(args.Entries) {
			// 长度不够，作为冲突处理
			conflict = true
		} else {
			for i, _ := range args.Entries {
				idx := args.PrevLogIndex + 1 + i
				if rf.log[idx].Term != args.Entries[i].Term {
					conflict = true
					break
				}
			}
		}
		if conflict {
			rf.log = rf.log[:args.PrevLogIndex+1]
			rf.log = append(rf.log, args.Entries...)
		}
		DPrintf("Follower %d: matched log update to (%d / %d)\n", rf.me, args.PrevLogIndex+len(args.Entries), rf.lastLogIndex())
		rf.mu.Unlock()
	}

	// update follower's commitIndex
	if args.LeaderCommit > rf.commitIndex {
		rf.commitIndex = minInt(rf.lastLogIndex(), args.LeaderCommit)
		DPrintf("Follower %d update commitIndex to (%d / %d)\n", rf.me, rf.commitIndex, rf.lastLogIndex())
	}

	rf.appendCh <- true

}

func (rf *Raft) sendAppendEntries(server int, args *AppendEntriesArgs, reply *AppendEntriesReply) bool {
	ok := rf.peers[server].Call("Raft.AppendEntries", args, reply)
	return ok
}

/********
 * APIs *
 ********/

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
		rf.log = append(rf.log, LogEntry{Index: index, Term: term, Command: command})
		// DPrintf("Append 1 entry to leader %d, log update to %s\n", rf.me, rf.logToString())
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
	// 设为 -1，因为 server 编号从 0 开始
	rf.votedFor = -1
	rf.state = FOLLOWER
	// 必须初始化，即使是无缓冲的 channel
	// 否则一直阻塞，但是不会报错
	rf.appendCh = make(chan bool)
	rf.voteCh = make(chan bool)
	rf.log = make([]LogEntry, 1) // start from 1
	rf.nextIndex = make([]int, len(rf.peers))
	rf.matchIndex = make([]int, len(rf.peers))
	rf.applyCh = applyCh

	rf.randResetTimer()
	go func() {
		for {
			switch rf.state {
			case FOLLOWER:
				select {
				// 阻塞直到其中一个 case 成立
				case <-rf.appendCh:
					rf.randResetTimer()
				case <-rf.voteCh:
					rf.randResetTimer()
				case <-rf.electionTimer.C:
					// 超时
					rf.updateStateTo(CANDIDATE)
					rf.startElection()
				}
			case CANDIDATE:
				select {
				case <-rf.appendCh:
					// 其他服务器已经成为 LEADER
					rf.updateStateTo(FOLLOWER)
				case <-rf.electionTimer.C:
					// 超时，新一轮选举
					rf.startElection()
				default:
					// avoid race
					// if (rf.voteCount > len(rf.peers)/2) {
					var win bool
					rf.mu.Lock()
					if rf.voteCount > len(rf.peers)/2 {
						win = true
					}
					rf.mu.Unlock()
					if win == true {
						// 赢得选举
						DPrintf("server %d got %d out of %d vote, become LEADER, term = %d\n",
							rf.me, rf.voteCount, len(rf.peers), rf.currentTerm)
						rf.updateStateTo(LEADER)
						// rf.maintainAuthority()
					} else {
						// DPrintf("server %d only got %d out of %d vote ,remain CANDIDATE\n", rf.me, rf.voteCount, len(rf.peers))
					}
				}
			case LEADER:
				// Part B: update rf.commitIndex
				for i := rf.lastLogIndex(); i > rf.commitIndex; i-- {
					count := 1 // 初始化为 1 因为自身也有一票
					for server, v := range rf.matchIndex {
						if server == rf.me {
							continue
						}
						if v >= i {
							count++
						}
					}
					if count > len(rf.peers)/2 {
						// majority
						rf.commitIndex = i
						DPrintf("Leader %d update commitIndex to %d, lastApplied = %d\n", rf.me, rf.commitIndex, rf.lastApplied)
						break
					}
				}

				rf.broadcastAppendEntries()
				time.Sleep(HEARTBEAT_INTERVAL)
			} // end of switch

			go rf.applyLog()
		}
	}()

	// initialize from state persisted before a crash
	rf.readPersist(persister.ReadRaftState())

	return rf
}

/*****************
 * ToolFunctions *
 *****************/

func (rf *Raft) lastLogIndex() int {
	// -1 is safe because rf.log is initialized to has 1 entry
	return rf.log[len(rf.log)-1].Index
}

func (rf *Raft) logToString() string {
	var buf bytes.Buffer
	buf.WriteString("[ ")
	for _, entry := range rf.log {
		buf.WriteString(fmt.Sprintf("%v(%d) ", entry.Command, entry.Term))
	}
	buf.WriteString("]")
	return buf.String()
}

func (rf *Raft) applyLog() {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	if rf.commitIndex > rf.lastApplied {
		for i := rf.lastApplied + 1; i <= rf.commitIndex; i++ {
			rf.applyCh <- ApplyMsg{Index: i, Command: rf.log[i].Command}
		}
		DPrintf("Server %d applied %d entry, now (%d / %d)\n",
			rf.me, rf.commitIndex-rf.lastApplied, rf.commitIndex, rf.lastLogIndex())
		rf.lastApplied = rf.commitIndex
	} else {
		/*
			if rf.lastLogIndex() > rf.commitIndex {
				fmt.Printf("Can not apply log from %d to %d on server %d\n", rf.commitIndex+1, rf.lastLogIndex(), rf.me)
			}
		*/
	}
}

func minInt(arr ...int) int {
	// error, return -1. Not good, but...
	if len(arr) == 0 {
		return -1
	}

	curMin := arr[0]
	for _, v := range arr {
		if v < curMin {
			curMin = v
		}
	}
	return curMin
}

func randInt64InRange(min, max int64) int64 {
	return min + rand.Int63n(max-min)
}

func (rf *Raft) randResetTimer() {
	if rf.electionTimer == nil {
		rf.electionTimer = time.NewTimer(time.Duration(randInt64InRange(MIN_ELECTION_INTERVAL, MAX_ELECTION_INTERVAL)) * time.Millisecond)
	} else {
		rf.electionTimer.Reset(time.Duration(randInt64InRange(MIN_ELECTION_INTERVAL, MAX_ELECTION_INTERVAL)) * time.Millisecond)
	}
}

func (rf *Raft) startElection() {
	// this function is called when a follower becomes a candidate
	rf.mu.Lock()
	rf.currentTerm += 1
	rf.votedFor = rf.me // vote for self
	rf.voteCount = 1
	rf.randResetTimer() // reset timer
	rf.mu.Unlock()
	args := RequestVoteArgs{}
	args.Term = rf.currentTerm
	args.CandidateId = rf.me
	// Part B
	args.LastLogIndex = rf.lastLogIndex()
	args.LastLogTerm = rf.log[rf.lastLogIndex()].Term

	DPrintf("Candidate %d start new election, term = %d\n", rf.me, rf.currentTerm)
	for i, _ := range rf.peers {
		// skip self
		if i == rf.me {
			continue
		}
		// every reply is different, so put it in goroutine
		go func(server int) {
			var reply RequestVoteReply
			if rf.sendRequestVote(server, &args, &reply) {
				if reply.VoteGranted == true {
					rf.mu.Lock()
					rf.voteCount += 1
					rf.mu.Unlock()
				} else {
					// response contains Term > currentTerm
					if reply.Term > rf.currentTerm {
						rf.mu.Lock()
						rf.currentTerm = reply.Term
						rf.updateStateTo(FOLLOWER)
						rf.mu.Unlock()
					}
				}
			}
		}(i)
	}
}

func (rf *Raft) broadcastAppendEntries() {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	for i, _ := range rf.peers {
		if rf.state != LEADER {
			// 状态随时都可能被其他 goroutine 修改
			return
		}
		if i == rf.me {
			// skip self
			continue
		}
		go func(server int) {
			args := AppendEntriesArgs{}
			reply := AppendEntriesReply{}
		LOOP:
			args.Term = rf.currentTerm
			args.LeaderId = rf.me
			args.PrevLogIndex = rf.nextIndex[server] - 1
			args.PrevLogTerm = rf.log[args.PrevLogIndex].Term
			args.LeaderCommit = rf.commitIndex
			if rf.lastLogIndex() >= rf.nextIndex[server] {
				args.Entries = rf.log[rf.nextIndex[server]:]
			}
			// fmt.Printf("Current Leader log: %s\n", rf.logToString())
			if rf.sendAppendEntries(server, &args, &reply) {
				// fmt.Printf("Leader %d send heartbeat to %d\n", rf.me, server)
				if reply.Success == true {
					rf.nextIndex[server] += len(args.Entries)
					rf.matchIndex[server] = rf.nextIndex[server] - 1
				} else {
					/*
						这样判断有一个很大的问题。由于 Leader 是并发的，会收到好几个 follower 的答复
						第一个收到的 follower 的答复导致了 rf.currentTerm 更新
						那么此后的答复都会是 reply.Term == rf.currentTerm
						因此不能简单据此判断是 log entry 不合
						if reply.Term > rf.currentTerm {
							// fail because of outdate
							rf.mu.Lock()
							rf.currentTerm = reply.Term
							rf.updateStateTo(FOLLOWER)
							rf.mu.Unlock()
						} else {
							// fail because of log inconsistency
							// decrement nextIndex and retry
							fmt.Printf("Leader %d: duplicate to follower %d failed, retry...\n", rf.me, server)
							rf.nextIndex[server] -= 1
							goto LOOP
						}
					*/
					if reply.Term > rf.currentTerm {
						// fail because of outdate
						rf.mu.Lock()
						rf.currentTerm = reply.Term
						rf.updateStateTo(FOLLOWER)
						rf.mu.Unlock()
					}
					// 非常关键的判断，不加会导致过时的 Leader 仍然能进行 log replication
					if rf.state != LEADER {
						return
					}
					// 如果返回 false, 而且不是因为 Term 过时，则肯定是 log 不匹配
					// fail because of log inconsistency
					// decrement nextIndex and retry
					rf.nextIndex[server] -= 1
					DPrintf("Leader %d: duplicate to follower %d failed, retry from %d...\n", rf.me, server, rf.nextIndex[server])
					goto LOOP
				}
			} else {
				// DPrintf("Network Error, Leader %d cannot reach server %d\n", rf.me, server)
			}
		}(i)
	}
}

func (rf *Raft) updateStateTo(state int) {
	if state == rf.state {
		return
	}
	stateDesc := []string{"FOLLOWER", "CANDIDATE", "LEADER"}
	preState := rf.state
	switch state {
	case FOLLOWER:
		rf.state = FOLLOWER
	case CANDIDATE:
		rf.state = CANDIDATE
	case LEADER:
		rf.state = LEADER
		// Re-initialized after election
		for i, _ := range rf.peers {
			rf.nextIndex[i] = rf.lastLogIndex() + 1
			rf.matchIndex[i] = 0
		}
	default:
		fmt.Printf("Warning: invalid state %d, do nothing.\n", state)
	}
	DPrintf("In term %d: Server %d transfer from %s to %s\n",
		rf.currentTerm, rf.me, stateDesc[preState], stateDesc[rf.state])
}
