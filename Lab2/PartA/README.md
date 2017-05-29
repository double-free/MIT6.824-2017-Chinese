Part 2A
---
> Implement leader election and heartbeats (`AppendEntries` RPCs with no log entries). The goal for Part 2A is for a single leader to be elected, for the leader to remain the leader if there are no failures, and for a new leader to take over if the old leader fails or if packets to/from the old leader are lost. Run `go test -run 2A` to test your 2A code.

该部分主要是要求完成 server 选举的相关功能，暂时不牵涉到 log。重点阅读论文的 5.1 以及 5.2，结合 Figure 2 食用。
首先强调一下整体架构。在课程给出的框架体系中，`Raft` 这个结构体是每个 server 持有一个的，作为状态机的存在。整个运行中，只有两种 RPC，一种是请求投票的 `RequestVote`，一种是修改日志（也可以作为心跳包）的 `AppendEntries`。 每个 server 功能是完全一样的，只是状态不同而已。都是通过 `sendXXX()` 来调用其他 server 的 `XXX()` 方法，当然还需要指定发送给哪个 server。
个人做了一个最小实现，暂不引入用不到的 log 相关内容。
这个实现强调的是容易理解。总共不到 300 行代码。

首先为 Raft 增加了必要的 field。尤其注意两个 channel，是用于 goroutine 之间的通信，如果收到了 append 或者 vote 的信息，及时通知主循环。
```go
type Raft struct {
	mu        sync.Mutex          // Lock to protect shared access to this peer's state
	peers     []*labrpc.ClientEnd // RPC end points of all peers
	persister *Persister          // Object to hold this peer's persisted state
	me        int                 // this peer's index into peers[]

	// Your data here (2A, 2B, 2C).
	// Look at the paper's Figure 2 for a description of what
	// state a Raft server must maintain.

	currentTerm int	// 当前任期
	votedFor int	// 投票给了谁

	state int	// 目前自己是 FOLLOWER, CANDIDATE 还是 LEADER

	electionTimer *time.Timer	// timer

	appendCh chan bool
	voteCh chan bool

	voteCount int	// rf.me 收到的选票
}
```
下面重点分析一下主循环，这张图的主要逻辑根据 Figure 2 的 Rules for Servers 得到。它反映了 Raft 算法的本质。每个 server 都在运行同样的循环，根据此刻自身不同的 state 选择不同的行动方式。
```go
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
					rf.startElection();
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
					rf.startElection();
				default:
					// avoid race
					// if (rf.voteCount > len(rf.peers)/2) {
					var win bool
					rf.mu.Lock()
					if (rf.voteCount > len(rf.peers)/2) {
						win = true
					}
					rf.mu.Unlock()
					if (win == true) {
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
```
这个主循环实际就是实现了一张图：

![转化过程.png](http://upload-images.jianshu.io/upload_images/4482847-a56d75f416c97ed8.png?imageMogr2/auto-orient/strip%7CimageView2/2/w/1240)

Raft 算法的几个重要的思想也列举一下我的实现。

1. **随机的 election timeout**

主要是每个 raft 设置了一个 timer。初始化结束后就开始计时，然后进入主循环。此后在合适的时机 reset。
```go
// rand[min, max]
func randInt64InRange(min, max int64) int64 {
	return min + rand.Int63n(max-min)
}
// init or reset timer
func (rf *Raft) randResetTimer() {
	if (rf.electionTimer == nil) {
		rf.electionTimer = time.NewTimer(time.Duration(randInt64InRange(MIN_ELECTION_INTERVAL, MAX_ELECTION_INTERVAL)) * time.Millisecond)
	} else {
		rf.electionTimer.Reset(time.Duration(randInt64InRange(MIN_ELECTION_INTERVAL, MAX_ELECTION_INTERVAL)) * time.Millisecond)
	}
}
```

2. **CANDIDATE 开启投票以及处理投票结果**

注意所有的 RPC 都应该是并发的。
```go
func (rf *Raft) startElection() {
	// this function is called when a follower becomes a candidate
	rf.mu.Lock()
	rf.currentTerm += 1
	rf.votedFor = rf.me	// vote for self
	rf.voteCount = 1
	rf.randResetTimer()	// reset timer
	rf.mu.Unlock()
	args := RequestVoteArgs{Term: rf.currentTerm, CandidateId: rf.me}

	for i,_ := range rf.peers {
		// skip self
		if (i == rf.me) {
			continue
		}
		// every reply is different, so put it in goroutine
		go func (server int)  {
			var reply RequestVoteReply
			if (rf.sendRequestVote(server, &args, &reply)) {
				if reply.VoteGranted == true {
					rf.mu.Lock()
					rf.voteCount += 1
					rf.mu.Unlock()
				} else {
					// response contains Term > currentTerm
					if (reply.Term > rf.currentTerm) {
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
```

3. **收到投票请求时的处理逻辑**
```go
func (rf *Raft) RequestVote(args *RequestVoteArgs, reply *RequestVoteReply) {
	// Your code here (2A, 2B).
	/*
	rf.mu.Lock()
	defer rf.mu.Unlock()
	*/
	// 收到了过时的信息
	if (args.Term < rf.currentTerm) {
		reply.VoteGranted = false
		reply.Term = rf.currentTerm
		return
	}
	// 收到了新 term 的信息
	if (args.Term > rf.currentTerm) {
		rf.currentTerm = args.Term
		rf.updateStateTo(FOLLOWER)
		// 易错点
		// if it's a new term, just vote for the first candidate
		rf.votedFor = args.CandidateId
	}
	reply.Term = rf.currentTerm

	if (rf.votedFor == -1 || rf.votedFor == args.CandidateId) {
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
```

4. **发送心跳包**

由于还没有加入 log，这里的逻辑还是很简单的。
```go
func (rf *Raft) broadcastAppendEntries() {
	args := AppendEntriesArgs{Term: rf.currentTerm, LeaderId: rf.me}
	for i,_ := range rf.peers {
		if (i == rf.me) {
			// skip self
			continue
		}
		go func (server int) {
			var reply AppendEntriesReply
			if rf.sendAppendEntries(server, &args, &reply) {
				if reply.Success == true {

				} else {
					if (reply.Term > rf.currentTerm) {
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
```

5. **收到心跳包的处理逻辑**
```go
func (rf *Raft) AppendEntries(args *AppendEntriesArgs, reply *AppendEntriesReply) {
	// 收到过时信息
	if (args.Term < rf.currentTerm) {
		reply.Success = false
		reply.Term = rf.currentTerm
		return
	}
	// 收到新 term 信息
	if (args.Term > rf.currentTerm) {
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
```

注意，以上实现都没有牵涉到 log。运行 `go test -run 2A` 测试结果如下。
```
Test (2A): initial election ...
In term 0: Server 0 transfer from FOLLOWER to CANDIDATE
server 0 become CANDIDATE, term = 1
server 0 got 2 out of 3 vote, become LEADER, term = 1
In term 1: Server 0 transfer from CANDIDATE to LEADER
  ... Passed
Test (2A): election after network failure ...
In term 0: Server 0 transfer from FOLLOWER to CANDIDATE
server 0 become CANDIDATE, term = 1
In term 0: Server 2 transfer from FOLLOWER to CANDIDATE
server 2 become CANDIDATE, term = 1
server 0 got 2 out of 3 vote, become LEADER, term = 1
In term 1: Server 0 transfer from CANDIDATE to LEADER
In term 1: Server 2 transfer from CANDIDATE to FOLLOWER
********** Leader 0 disconnect... **********
In term 1: Server 1 transfer from FOLLOWER to CANDIDATE
server 1 become CANDIDATE, term = 2
server 1 got 2 out of 3 vote, become LEADER, term = 2
In term 2: Server 1 transfer from CANDIDATE to LEADER
********** Leader 0 reconnect... **********
In term 2: Server 0 transfer from LEADER to FOLLOWER
In term 2: Server 0 transfer from FOLLOWER to CANDIDATE
server 0 become CANDIDATE, term = 3
In term 3: Server 1 transfer from LEADER to FOLLOWER
server 0 got 2 out of 3 vote, become LEADER, term = 3
In term 3: Server 0 transfer from CANDIDATE to LEADER
********** No quorum...no leader **********
In term 3: Server 1 transfer from FOLLOWER to CANDIDATE
server 1 become CANDIDATE, term = 4
In term 3: Server 2 transfer from FOLLOWER to CANDIDATE
server 2 become CANDIDATE, term = 4
********** Quorum arise...one leader **********
In term 8: Server 1 transfer from CANDIDATE to FOLLOWER
server 2 got 2 out of 3 vote, become LEADER, term = 8
In term 8: Server 2 transfer from CANDIDATE to LEADER
********** Re-join node...one leader **********
In term 8: Server 0 transfer from LEADER to FOLLOWER
In term 8: Server 0 transfer from FOLLOWER to CANDIDATE
server 0 become CANDIDATE, term = 9
In term 9: Server 2 transfer from LEADER to FOLLOWER
server 0 got 2 out of 3 vote, become LEADER, term = 9
In term 9: Server 0 transfer from CANDIDATE to LEADER
In term 9: Server 2 transfer from FOLLOWER to CANDIDATE
server 2 become CANDIDATE, term = 10
In term 10: Server 0 transfer from LEADER to FOLLOWER
server 2 got 2 out of 3 vote, become LEADER, term = 10
In term 10: Server 2 transfer from CANDIDATE to LEADER
  ... Passed
PASS
ok  	raft	7.022s
```

个人总结
---
水平比较渣，杂事又多，断断续续搞了好几天，但是还是算独立做出来了。
#### 难点
1. 并发程序的调试，Go 语言特性的掌握。
2. 理解哪些数据是选举过程必须使用的，哪些是暂时用不着的。

一个调试小技巧，修改状态用一个函数实现，可以观察每次状态变化，非常有利于调试。
```go
func (rf *Raft) updateStateTo(state int) {
	if (state == rf.state) {
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
```
