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

//
// example RequestVote RPC arguments structure.
// field names must start with capital letters!
//
type RequestVoteArgs struct {
	// Your data here (2A, 2B).
	Term        int
	CandidateId int
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
	// 在这里加锁导致死锁
	// 因为这个 goroutine 想要往 votech 里写东西
	// 而 Make 中的 goroutine 想要读
	/*
		rf.mu.Lock()
		defer rf.mu.Unlock()
	*/

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

	if rf.votedFor == -1 || rf.votedFor == args.CandidateId {
		// first come first served
		rf.votedFor = args.CandidateId
		reply.VoteGranted = true
		go func() {
			DPrintf("server %d received RequestVote from CANDIDATE %d, vote for %d\n", rf.me, args.CandidateId, rf.votedFor)
			rf.voteCh <- true
		}()
	} else {
		reply.VoteGranted = false
	}
}

/*****************
 * AppendEntries *
 *****************/

type AppendEntriesArgs struct {
	Term     int
	LeaderId int
}

type AppendEntriesReply struct {
	Term    int
	Success bool
}

func (rf *Raft) AppendEntries(args *AppendEntriesArgs, reply *AppendEntriesReply) {
	if args.Term < rf.currentTerm {
		reply.Success = false
		reply.Term = rf.currentTerm
		return
	}
	if args.Term > rf.currentTerm {
		rf.currentTerm = args.Term
		rf.updateStateTo(FOLLOWER)
	}
	reply.Success = true
	reply.Term = rf.currentTerm
	go func() {
		DPrintf("server %d(Term = %d) received AppendEntries from LEADER %d(Term = %d)\n",
			rf.me, rf.currentTerm, args.LeaderId, args.Term)
		rf.appendCh <- true
	}()

}

func (rf *Raft) sendAppendEntries(server int, args *AppendEntriesArgs, reply *AppendEntriesReply) bool {
	ok := rf.peers[server].Call("Raft.AppendEntries", args, reply)
	return ok
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
	// 否则一直无法收到数据
	rf.appendCh = make(chan bool)
	rf.voteCh = make(chan bool)

	rf.randResetTimer()
	go func() {
		for {
			switch rf.state {
			case FOLLOWER:
				select {
				// 阻塞直到其中一个 case 成立
				case <-rf.appendCh:
					DPrintf("received append request, reset timer for server %d.\n", rf.me)
					rf.randResetTimer()
				case <-rf.voteCh:
					DPrintf("received vote request, reset timer for server %d.\n", rf.me)
					rf.randResetTimer()
				case <-rf.electionTimer.C:
					// 超时
					rf.updateStateTo(CANDIDATE)
					rf.startElection()
					fmt.Printf("server %d become CANDIDATE, term = %d\n", rf.me, rf.currentTerm)
				}
			case CANDIDATE:
				select {
				case <-rf.appendCh:
					// 其他服务器已经成为 LEADER
					DPrintf("server %d become FOLLOWER", rf.me)
					rf.updateStateTo(FOLLOWER)
				case <-rf.electionTimer.C:
					// 超时，新一轮选举
					DPrintf("New Election Started...")
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
						fmt.Printf("server %d got %d out of %d vote, become LEADER, term = %d\n",
							rf.me, rf.voteCount, len(rf.peers), rf.currentTerm)
						rf.updateStateTo(LEADER)
						// rf.maintainAuthority()
					} else {
						// DPrintf("server %d only got %d out of %d vote ,remain CANDIDATE\n", rf.me, rf.voteCount, len(rf.peers))
					}
				}
			case LEADER:
				rf.broadcastAppendEntries()
				time.Sleep(HEARTBEAT_INTERVAL)
			}
		}
	}()

	// initialize from state persisted before a crash
	rf.readPersist(persister.ReadRaftState())

	return rf
}

func (rf *Raft) startElection() {
	// this function is called when a follower becomes a candidate
	rf.mu.Lock()
	rf.currentTerm += 1
	rf.votedFor = rf.me // vote for self
	rf.voteCount = 1
	rf.randResetTimer() // reset timer
	rf.mu.Unlock()
	args := RequestVoteArgs{Term: rf.currentTerm, CandidateId: rf.me}

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
	args := AppendEntriesArgs{Term: rf.currentTerm, LeaderId: rf.me}
	for i, _ := range rf.peers {
		if i == rf.me {
			// skip self
			continue
		}
		go func(server int) {
			var reply AppendEntriesReply
			if rf.sendAppendEntries(server, &args, &reply) {
				if reply.Success == true {

				} else {
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
	default:
		fmt.Printf("Warning: invalid state %d, do nothing.\n", state)
	}
	fmt.Printf("In term %d: Server %d transfer from %s to %s\n",
		rf.currentTerm, rf.me, stateDesc[preState], stateDesc[rf.state])
}
