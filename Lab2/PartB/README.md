Part 2B
---
在这个部分我们希望 Raft 能保持一个一致的操作 log。利用 `Start()` 给 leader 的 log 增加一个新的操作。Leader 会将这个新的操作通过 `AppendEntries` RPC 给其他的 server。

>**Task**
Implement the leader and follower code to append new log entries. This will involve implementing `Start()`, completing the `AppendEntries` RPC structs, sending them, fleshing out the `AppendEntry` RPC handler, and advancing the `commitIndex` at the leader. Your first goal should be to pass the `TestBasicAgree()` test (in `test_test.go`). Once you have that working, you should get all the 2B tests to pass (`go test -run 2B`).

论文阅读笔记
---
### 5.3 Log replication
一旦选举出了一个 leader，它就会开始服务 client 的请求。每个请求包含了一个需要被 replicated state machines 执行的命令。leader 将命令作为一个新 entry 添加到自己的 log 中，然后并行地发送 `AppendEntries` RPC 给其他的 server。确认该 entry 被安全地 replicate 后，再执行命令，并向 client 返回结果。 
所谓 "安全地 replicate"，是指大部分的 server 都已经复制了这个 entry。这样的 entry 被称为 "committed"，Leader 需要记录并更新 log 中最大的 committed 序号。还需要将其发送给 follower。
对于每个 server，commit 过后的 entry，就可以 apply 了。
一个典型的 LogEntry 可定义如下：
```go
type LogEntry struct {
	Index   int // Start from 1
	Term    int
	Command interface{} // Same as the input in Start()
}
```
个人认为，之所以从编号 1 开始，是为了描述 “两份 log 中没有一个 entry 相同” 时，不用特殊处理。因为编号 0 都是一样的默认初始化值，可作为哨兵。
```go
// Raft 结构体中增加的部分
	// Part B
	// 
	log         []LogEntry
	// log[1 : commitIndex] 是大部分 server 都达成一致的
	commitIndex int
	// log[1 : lastApplied] 是自己已经执行了的
	lastApplied int
	// nextIndex[i] 表示第 i 个 server 上复制 LogEntry 的起点
	nextIndex   []int
	// matchIndex[i] 表示第 i 个 server 上已经和 leader 一致的最高序号
	// 理想情况下有 matchIndex[i] = nextIndex[i] - 1
	matchIndex  []int
	applyCh     chan ApplyMsg
```
需要注意的是，`nextIndex` 以及 `matchIndex` 两个 slice 只有在该 server 是 Leader 时才有作用。用于记录各个 follower 的 log 同步情况。
```go
type AppendEntriesArgs struct {
	// Part A
	Term     int
	LeaderId int

	// Part B
	// leader 认为的该 follower 最后一个匹配的位置。
	PrevLogIndex int
	// 该位置上的 entry 的 Term 属性
	PrevLogTerm  int
	// leader 的 log[PrevLogIndex+1 : ]
	Entries      []LogEntry
	// 将 leader 的 commitIndex 通知各个 follower
	// follower 将据此更新自身的 commitIndex
	LeaderCommit int  
}
```
这是 Part B 的核心，Leader 通过这个结构体来与各个 Follower 同步 log。目前这些注释可能比较费解，但是真正开始写程序后就能明白其真实的含义。

### 5.4.1 Election restriction
仅仅以上的步骤，还不足以确保正确性。如果所有 follower 都只是盲从于 leader，那么假设某个 offline 过一段时间的 server，重新上线后不久被选为了 leader，那么它可能就会改写其他所有成员的log，导致某些 committed 的 entry 被覆盖，出现错误的结果。
因此需要改写投票时使用的结构体为：
```go
type RequestVoteArgs struct {
	// Your data here (2A, 2B).
	Term        int
	CandidateId int
	// Part B
	LastLogIndex int
	LastLogTerm  int
}
```
也就是说，投票时，Candidate 需要自报家门，告诉 Follower 自己最新的 log，Follower 如果发现自己的信息比较新，就不再给这个 Candidate 投票。

理解与实现
---
首先需要深刻理解的是运作流程。其中涉及到几个状态的理解。

- **committed**

其含义是，该分布式系统 log 中已经达成一致的部分。是可以进行下一步 apply 的部分。

- **applied**

已经committed，并发送到 applyCh 信道的 log 部分。

client 通过 `Start()` 指派指令，Leader 收到指令后，只是将其追加到 log 中，暂时不发送，直到下一次 `broadcastAppendEntries()` 时再发送。也就是说，是凭借心跳包发出的。如果这个时段没有追加新指令，则就是单纯的心跳包，否则就是带了数据的。
Follower 接收到 AppendEntries 这个RPC，会首先找到同步的 log，然后将新指令追加到之后。参数还包括 Leader 此时的 commitIndex，会用来更新 Follower 的 commitIndex。这是为了让所有 Follower 与 Leader 进度保持一致。注意，只有 Leader 的 commitIndex 是根据 matchIndex 进行主动更新的，所有 Follower 都是通过 AppendEntries 中的 LeaderCommit 来被动更新。
当达成一致以后，就能 apply 了。apply 的方法其实就是将命令打包成 ApplyMsg，并发给每个 server 自己的 applyCh。

理清了这些关系，写出一个能通过 BasicAgree 测试的程序就不难了。
此后就是不断地 debug。我遇到的问题记录在后半部分，希望有所启发。

还需要阅读 `raft/config.go` 中的 `one()` 函数，该函数会在整个测试用例中频繁使用。
```go
func (cfg *config) one(cmd int, expectedServers int) int {
// 参数1: 需要达成 agreement 的命令
// 参数2: 期望有几个 server 达成一致
// 返回: 同步的 cmd 在 log 中的序号，-1 表示错误
	t0 := time.Now()
	starts := 0
  // 在 10s 内不断重复以下过程。注意每次 sleep 一段时间。
	for time.Since(t0).Seconds() < 10 {
		// try all the servers, maybe one is the leader.
		index := -1
		for si := 0; si < cfg.n; si++ {
      // 遍历所有 server，找到 Leader 并开始一个新的 agreement (cmd)
			starts = (starts + 1) % cfg.n
			var rf *Raft
			cfg.mu.Lock()
			if cfg.connected[starts] {
				rf = cfg.rafts[starts]
			}
			cfg.mu.Unlock()
			if rf != nil {
				index1, _, ok := rf.Start(cmd)
				if ok {
					index = index1
					break
				}
			}
		}

		if index != -1 {
			// somebody claimed to be the leader and to have
			// submitted our command; wait a while for agreement.
			t1 := time.Now()
			for time.Since(t1).Seconds() < 2 {
				nd, cmd1 := cfg.nCommitted(index)
				if nd > 0 && nd >= expectedServers {
					// committed
					if cmd2, ok := cmd1.(int); ok && cmd2 == cmd {
						// and it was the command we submitted.
						return index
					}
				}
				time.Sleep(20 * time.Millisecond)
			}
		} else {
			time.Sleep(50 * time.Millisecond)
		}
	}
	cfg.t.Fatalf("one(%v) failed to reach agreement", cmd)
	return -1
}
```


问题记录
---
1. **RPC 参数传递出现 nil 指针**

```
args at 0x0, reply at 0xc420011f50
panic: runtime error: invalid memory address or nil pointer dereference
```
发现是由于 LogEntry 中的 field 没有大写导致的。传递的 args 中包含了一个 LogEntry 的 slice。因此 LogEntry 中的所有 field 也应该大写。
```go
type LogEntry struct {
	Index   int // Start from 1
	Term    int
	Command interface{} // Same as the input in Start()
}
```

2. **applyCh 阻塞**

代码重构了一次，忘了初始化 rf.applyCh。第二次遇到这类问题了。

3. **能通过 BasicAgree, 不能通过 FailAgree**

查了很久，发现原因在更新 commitIndex 上。Leader 自身还有一票，所以 count 应该初始化为 1。
```go
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
```
这里有人可能会问，为什么不在更新 follower 的 nextIndex 和 matchIndex 时顺便更新 Leader 的，避免这里的特殊处理？
原因在于，每个 follower 的进度都不一样，到底按照谁的来更新呢？取最大当然可行，但是太麻烦了。本质上，Leader 的数据总是 up-to-date 的，所以这样处理比较好。

4. **通过了 FailAgree 的 105，但是 106 开始 Fail**

出现的状况是无法选举出 Leader。于是在 vote 过程排查。发现又是一个低级错误，在 `startElection()` 函数中忘了初始化两个新的field。细节，细节，细节。
```go
args := RequestVoteArgs{}
args.Term = rf.currentTerm
args.CandidateId = rf.me
// Part B
args.LastLogIndex = rf.lastLogIndex()
args.LastLogTerm = rf.log[rf.lastLogIndex()].Term
```

5. **无法通过 TestRejoin2B 的 103**

该测试场景是当 Leader 断开又重连时，系统能否正常运作。
分析 log 发现旧的 Leader (假设为 leader0) 重连后改变了某个 Follower 的 log，这本来是不该发生的。查错发现，leader0 重连后，由于自身状态还是 Leader，会发送心跳包。第一个心跳包返回后，会修改 leader0 的 Term 为现在的 term，并将其转为 follower。然而，由于并发，另一个心跳包返回时，该 leader 的currentTerm 已经是最新了，而由于这时没有判断 state == LEADER，所以直接进入了 log 复制环节。
```go
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
					rf.nextIndex[server] -= 1
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
```

7. **有时无法通过 TestBackup2B**

偶尔才出现，不好排查。暂时作为遗留问题。
如下：
```
yuanyedeMacBook-Pro:raft Crow$ go test -run Backup2B
Test (2B): leader backs up quickly over incorrect follower logs ...
... Passed
PASS
ok  	raft	25.807s
yuanyedeMacBook-Pro:raft Crow$ go test -run Backup2B
Test (2B): leader backs up quickly over incorrect follower logs ...
2017/06/06 16:35:55 apply error: commit index=52 server=4 8236000563283034457 != server=2 3468179317868568601
exit status 1
FAIL	raft	25.682s
```

**原因(更新于 2017.06.09)**

找了一晚上，终于发现了 bug 的原因。

由于之前 offline 的服务器中存在 leader，后来再次接入的瞬间，整个网络存在两个自认为是 leader 的节点。旧的 leader 也会发送 AppendEntries，而某些节点接受了旧的 leader 的心跳包，改写了自己的 log。

原则上这是不应该发生的。因为旧的 leader 肯定 Term 也是旧的，发出的心跳包直接返回了。

但是一定要注意，这里是并行场景。

之前的代码结构为：
```go
func (rf *Raft) broadcastAppendEntries() {
	for i, _ := range rf.peers {
		if rf.state != LEADER {
			// 状态随时都可能被其他 goroutine 修改
			// 但是在这里执行检查时没有用的
			return
		}
		if i == rf.me {
			// skip self
			continue
		}
		// 对每个 server 进行 RPC
		go func(server int) {
			...
```
虽然当时意识到 server 的状态是不断变化的，但是理解还不够深入，导致在错误的地方进行检查。实际上在那里添加检查毫无用处。
也就是说，很有可能出现如下的情况：

goroutine A 是最先获得 RPC 返回的，因此会发现旧 leader 的 Term 过时了，将其转为 follower。
```go
...
if reply.Term > rf.currentTerm {
	// fail because of outdate
	DPrintf("Leader %d outdate to follower %d, (Term %d < %d)...\n", rf.me, server, rf.currentTerm, reply.Term)
	rf.mu.Lock()
	rf.currentTerm = reply.Term
	rf.updateStateTo(FOLLOWER)
	rf.mu.Unlock()
}
...
```
但是另一个 goroutine B 已经通过了最初的校验顺利开启，而且认为自己仍然是 LEADER。于是照常进行下一步初始化参数，并发起 RPC。
```go
...
args.Term = rf.currentTerm  // 这里的 currentTerm 已经被 goroutine A 更新过
args.LeaderId = rf.me
args.PrevLogIndex = rf.nextIndex[server] - 1
args.PrevLogTerm = rf.log[args.PrevLogIndex].Term
args.LeaderCommit = rf.commitIndex
if rf.lastLogIndex() >= rf.nextIndex[server] {
	args.Entries = rf.log[rf.nextIndex[server]:]
}
if rf.sendAppendEntries(server, &args, &reply) {
	...
}
```
这时候就会发生一个尴尬的状况，旧的 LEADER 的 Term 已经更新到最新，实际已经成为了 FOLLOWER，但是还是发送出了心跳包。那么作为 follower 肯定就无法甄别旧 leader 和新 leader 了。导致了出错。

实际上应该在发起RPC时添加校验：
```
// 每次发送 RPC 之前，都要确保自己是 LEADER
if rf.state == LEADER && rf.sendAppendEntries(server, &args, &reply) {
	...
}
```
再引申一下，有没有可能判断完了 `rf.state == LEADER` 成功后，被修改为 FOLLOWER，然后又执行了 RPC 呢？
当然可能，但是这是无所谓的，因为参数 `args` 已经确定了。 args.Term 是它尚在 LEADER 期时确定的，也就是说 Term 肯定不是最新的。

总结
---
这部分应该是 Lab2 的核心了。难度比较高，调试起来也复杂，重点还是多用 printf 输出观察，没有别的捷径。
