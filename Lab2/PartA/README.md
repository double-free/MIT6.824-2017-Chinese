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

	votedFor     int
	voteAcquired int
	state        int32
	currentTerm  int32

	electionTimer *time.Timer
	voteCh        chan struct{} // 成功投票的信号
	appendCh      chan struct{} // 成功更新 log 的信号
}
```
下面重点分析一下主循环，这张图的主要逻辑根据 Figure 2 的 Rules for Servers 得到。它反映了 Raft 算法的本质。每个 server 都在运行同样的循环，根据此刻自身不同的 state 选择不同的行动方式。
```go
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
			time.Sleep(HEARTBEAT_INTERVAL * time.Millisecond)
		}
	}
}
```
这个主循环实际就是实现了一张图：

![转化过程.png](http://upload-images.jianshu.io/upload_images/4482847-a56d75f416c97ed8.png?imageMogr2/auto-orient/strip%7CimageView2/2/w/1240)

Raft 算法的几个重要的思想也列举一下我的实现。

1. **随机的 election timeout**

主要是每个 raft 设置了一个 timer。初始化结束后就开始计时，然后进入主循环。此后在合适的时机 reset。
```go
func randElectionDuration() time.Duration {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	return time.Millisecond * time.Duration(r.Int63n(MAX_ELECTION_INTERVAL-MIN_ELECTION_INTERVAL)+MIN_ELECTION_INTERVAL)
}
```

2. **CANDIDATE 开启投票以及处理投票结果**
在修改 rf 的变量时，需要注意线程安全。使用互斥锁或者原子操作。

选举逻辑：
1. 进入下一term
2. 投票给自己，并且重置收到的选票
3. 设定选举持续时间
4. 要求所有其他 server 投票
```go
func (rf *Raft) startElection() {
	// should always been protected by lock

	rf.incrementTerm()
	rf.votedFor = rf.me
	rf.voteAcquired = 1
	rf.electionTimer.Reset(randElectionDuration())
	rf.broadcastVoteReq()
}
```

注意所有的 RPC 都应该是并发的。
```go
func (rf *Raft) broadcastVoteReq() {
	args := RequestVoteArgs{Term: atomic.LoadInt32(&rf.currentTerm), CandidateId: rf.me}
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
```

3. **收到投票请求时的处理逻辑**
```go
func (rf *Raft) RequestVote(args *RequestVoteArgs, reply *RequestVoteReply) {
	// Your code here (2A, 2B).
	rf.mu.Lock()
	defer rf.mu.Unlock()
	if args.Term < rf.currentTerm {
		reply.VoteGranted = false
		reply.Term = rf.currentTerm
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
	if reply.VoteGranted == true {
		go func() { rf.voteCh <- struct{}{} }()
	}
}
```

4. **发送心跳包**

由于还没有加入 log，这里的逻辑还是很简单的。
```go
func (rf *Raft) broadcastAppendEntries() {
	args := AppendEntriesArgs{Term: atomic.LoadInt32(&rf.currentTerm), LeaderId: rf.me}
	for i, _ := range rf.peers {
		if i == rf.me {
			continue
		}
		go func(server int) {
			var reply AppendEntriesReply
			if rf.isState(LEADER) && rf.sendAppendEntries(server, &args, &reply) {
				rf.mu.Lock()
				defer rf.mu.Unlock()
				if reply.Success == true {

				} else {
					if reply.Term > rf.currentTerm {
						rf.currentTerm = reply.Term
						rf.updateStateTo(FOLLOWER)
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
	rf.mu.Lock()
	defer rf.mu.Unlock()

	if args.Term < rf.currentTerm {
		reply.Success = false
		reply.Term = rf.currentTerm
	} else if args.Term > rf.currentTerm {
		rf.currentTerm = args.Term
		rf.updateStateTo(FOLLOWER)
		reply.Success = true
	} else {
		reply.Success = true
	}
	go func() { rf.appendCh <- struct{}{} }()
}
```

注意，以上实现都没有牵涉到 log。运行 `go test -run 2A` 测试通过。
```
Test (2A): initial election ...
In term 1: Server 1 transfer from FOLLOWER to CANDIDATE
In term 1: Server 1 transfer from CANDIDATE to LEADER
  ... Passed
Test (2A): election after network failure ...
In term 1: Server 0 transfer from FOLLOWER to CANDIDATE
In term 1: Server 0 transfer from CANDIDATE to LEADER
In term 2: Server 2 transfer from FOLLOWER to CANDIDATE
In term 2: Server 2 transfer from CANDIDATE to LEADER
In term 2: Server 0 transfer from LEADER to FOLLOWER
In term 3: Server 0 transfer from FOLLOWER to CANDIDATE
In term 3: Server 0 transfer from CANDIDATE to FOLLOWER
Server 0 send vote req failed.
In term 3: Server 2 transfer from LEADER to FOLLOWER
In term 4: Server 1 transfer from FOLLOWER to CANDIDATE
In term 4: Server 0 transfer from FOLLOWER to CANDIDATE
In term 4: Server 1 transfer from CANDIDATE to LEADER
In term 4: Server 0 transfer from CANDIDATE to FOLLOWER
In term 5: Server 0 transfer from FOLLOWER to CANDIDATE
In term 5: Server 2 transfer from FOLLOWER to CANDIDATE
Server 0 send vote req failed.
In term 9: Server 2 transfer from CANDIDATE to FOLLOWER
In term 9: Server 0 transfer from CANDIDATE to LEADER
In term 9: Server 1 transfer from LEADER to FOLLOWER
In term 10: Server 1 transfer from FOLLOWER to CANDIDATE
In term 10: Server 0 transfer from LEADER to FOLLOWER
In term 10: Server 1 transfer from CANDIDATE to LEADER
Server 2 send vote req failed.
Server 2 send vote req failed.
```


个人总结
---
水平比较渣，杂事又多，断断续续搞了好几天，但是还是算独立做出来了。
#### 难点
1. 并发程序的调试，Go 语言特性的掌握。
2. 理解哪些数据是选举过程必须使用的，哪些是暂时用不着的。

一个调试小技巧，修改状态用一个函数实现，可以观察每次状态变化，非常有利于调试。
```go
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
		rf.state = LEADER
	default:
		fmt.Printf("Warning: invalid state %d, do nothing.\n", state)
	}
	fmt.Printf("In term %d: Server %d transfer from %s to %s\n",
		rf.currentTerm, rf.me, stateDesc[preState], stateDesc[rf.state])
}
```
