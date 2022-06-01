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
	//	"bytes"

	"bytes"
	"sync"
	"sync/atomic"
	"time"

	"6.824/labgob"
	"6.824/labrpc"
)

//
// as each Raft peer becomes aware that successive log entries are
// committed, the peer should send an ApplyMsg to the service (or
// tester) on the same server, via the applyCh passed to Make(). set
// CommandValid to true to indicate that the ApplyMsg contains a newly
// committed log entry.
//
// in part 2D you'll want to send other kinds of messages (e.g.,
// snapshots) on the applyCh, but set CommandValid to false for these
// other uses.
//

const (
	LEADER    = 0
	CANDIDATE = 1
	FOLLOWER  = 2
)

var roler_string map[int]string

const (
	ELECTION_TIMER_RESOLUTION = 5 // check whether timer expire every 5 millisecond.
	// vote expire time range. (millsecond)
	ELECTION_EXPIRE_LEFT  = 200
	ELECTION_EXPIRE_RIGHT = 400
	// heartbeat time (millsecond)
	APPEND_EXPIRE_TIME      = 100
	APPEND_TIMER_RESOLUTION = 2
)

type ApplyMsg struct {
	CommandValid bool
	Command      interface{}
	CommandIndex int

	// For 2D:
	SnapshotValid bool
	Snapshot      []byte
	SnapshotTerm  int
	SnapshotIndex int
}

type LogEntry struct {
	Index   int
	Term    int
	Command interface{}
}

func (rf *Raft) getFirstIndex() int {
	return rf.log[0].Index
}

func (rf *Raft) getFirstTerm() int {
	return rf.log[0].Term
}

func (rf *Raft) getLastTerm() int {
	return rf.log[len(rf.log)-1].Term
}

func (rf *Raft) getLastIndex() int {
	return rf.log[len(rf.log)-1].Index
}

func (rf *Raft) getTermByIndex(idx int) int {
	return rf.log[idx-rf.getFirstIndex()].Term
}

func (rf *Raft) getCommandByIndex(idx int) interface{} {
	return rf.log[idx-rf.getFirstIndex()].Command
}

//
// A Go object implementing a single Raft peer.
//
type Raft struct {
	mu sync.Mutex // Lock to protect shared access to this peer's state
	cv *sync.Cond // the cv for sync producer and consumer

	peers     []*labrpc.ClientEnd // RPC end points of all peers
	persister *Persister          // Object to hold this peer's persisted state
	me        int                 // this peer's index into peers[]
	dead      int32               // set by Kill()

	currentTerm    int
	votedFor       int // -1 represent null
	receiveVoteNum int
	log            []LogEntry

	roler int

	ElectionExpireTime time.Time   // election expire time
	AppendExpireTime   []time.Time // next send append time

	// Your data here (2A, 2B, 2C).
	// Look at the paper's Figure 2 for a description of what
	// state a Raft server must maintain.

	// index of highest log entry known to be commited
	// (initialized to 0, increase monotonically)
	commitIdx int

	// index of highest log entry applied to state machine
	// (initialized to 0, increase monotonically)
	lastApplied int

	// AppendEntries RPC should send peer_i the nextIndex[peer_i] log
	// to peer_i
	nextIndex []int

	// matchIndex[i] means the matched log of peer_i and
	// this leader is [1-matchIndex[i]]
	// the matchIndex changes with nextIndex
	// and it influences the update of commitIndex
	matchIndex []int

	// applyQueue
	applyQueue []ApplyMsg
}

// return currentTerm and whether this server
// believes it is the leader.
func (rf *Raft) GetState() (int, bool) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	term := rf.currentTerm
	isleader := rf.roler == LEADER
	DebugGetInfo(rf)
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
	// e := labgob.NewEncoder(w)
	// e.Encode(rf.xxx)
	// e.Encode(rf.yyy)
	// data := w.Bytes()
	// rf.persister.SaveRaftState(data)
	data := SerizeState(rf)
	rf.persister.SaveRaftState(data)
	Debug(dPersist, "S%d Persist States. T%d, votedFor:%d, log: %v", rf.me,
		rf.currentTerm, rf.votedFor, rf.log)
}

//
// restore previously persisted state.
//
func (rf *Raft) readPersist(data []byte) {
	if data == nil || len(data) < 1 { // bootstrap without any state?
		return
	}
	// Your code here (2C).
	// Example:
	// r := bytes.NewBuffer(data)
	// d := labgob.NewDecoder(r)
	// var xxx
	// var yyy
	// if d.Decode(&xxx) != nil ||
	//    d.Decode(&yyy) != nil {
	//   error...
	// } else {
	//   rf.xxx = xxx
	//   rf.yyy = yyy
	// }
	r := bytes.NewBuffer(data)
	d := labgob.NewDecoder(r)
	var currentTerm int
	var votedFor int
	var log []LogEntry
	if d.Decode(&currentTerm) != nil ||
		d.Decode(&votedFor) != nil ||
		d.Decode(&log) != nil {
		Debug(dError, "S%d Read Persist Error!", rf.me)
	} else {
		rf.currentTerm = currentTerm
		rf.votedFor = votedFor
		rf.log = log
		Debug(dPersist, "S%d ReadPersist. State: T%d, votedFor%d, log: %v", rf.me,
			rf.currentTerm, rf.votedFor, rf.log)
	}
}

// reset Election
func (rf *Raft) ResetElectionTimer() {
	rf.ElectionExpireTime = GetRandomExpireTime(ELECTION_EXPIRE_LEFT, ELECTION_EXPIRE_RIGHT)
	DebugResetELT(rf)
}

// reset heartBeat, imme mean whether send immediately
func (rf *Raft) ResetAppendTimer(idx int, imme bool) {
	t := time.Now()
	if !imme {
		t = t.Add(APPEND_EXPIRE_TIME * time.Millisecond)
	}
	rf.AppendExpireTime[idx] = t
	DebugResetHBT(rf, idx)
}

//
// A service wants to switch to snapshot.  Only do so if Raft hasn't
// have more recent info since it communicate the snapshot on applyCh.
//
func (rf *Raft) CondInstallSnapshot(lastIncludedTerm int, lastIncludedIndex int, snapshot []byte) bool {

	// Your code here (2D).

	return true
}

// the service says it has created a snapshot that has
// all info up to and including index. this means the
// service no longer needs the log through (and including)
// that index. Raft should now trim its log as much as possible.
func (rf *Raft) Snapshot(index int, snapshot []byte) {
	// Your code here (2D).

}

//
// example RequestVote RPC arguments structure.
// field names must start with capital letters!
//
type RequestVoteArgs struct {
	Term         int
	CandidateId  int
	LastLogIndex int
	LastLogTerm  int
	// Your data here (2A, 2B).
}

//
// example RequestVote RPC reply structure.
// field names must start with capital letters!
//
type RequestVoteReply struct {
	Term        int
	VoteGranted bool
	// Your data here (2A).
}

type AppendEntriesArgs struct {
	Term         int
	LeaderId     int
	PrevLogIndex int
	PrevLogTerm  int
	Entries      []LogEntry
	LeaderCommit int
}

type AppendEntriesReply struct {
	Term          int
	Success       bool
	ConflictTerm  int
	ConflictIndex int
}

//
// the service using Raft (e.g. a k/v server) wants to start
// agreement on the next command to be appended to Raft's log. if this
// server isn't the leader, returns false. otherwise start the
// agreement and return immediately. there is no guarantee that this
// command will ever be committed to the Raft log, since the leader
// may fail or lose an election. even if the Raft instance has been killed,
// this function should return gracefully.
//
// the first return value is the index that the command will appear at
// if it's ever committed. the second return value is the current
// term. the third return value is true if this server believes it is
// the leader.
//
func (rf *Raft) Start(command interface{}) (int, int, bool) {

	rf.mu.Lock()
	defer rf.mu.Unlock()
	if rf.killed() {
		return -1, -1, false
	}
	if rf.roler != LEADER {
		return -1, -1, false
	}

	// this is a live leader,
	// append this command to its log and return
	// the HBT timer will sync this log to other peers
	rf.log = append(rf.log, LogEntry{
		Index:   rf.getLastIndex() + 1,
		Term:    rf.currentTerm,
		Command: command,
	})
	DebugNewCommand(rf)
	rf.persist()
	return rf.getLastIndex(), rf.currentTerm, true
}

//
// the tester doesn't halt goroutines created by Raft after each test,
// but it does call the Kill() method. your code can use killed() to
// check whether Kill() has been called. the use of atomic avoids the
// need for a lock.
//
// the issue is that long-running goroutines use memory and may chew
// up CPU time, perhaps causing later tests to fail and generating
// confusing debug output. any goroutine with a long-running loop
// should call killed() to check whether it should stop.
//
func (rf *Raft) Kill() {
	atomic.StoreInt32(&rf.dead, 1)
	// Your code here, if desired.
}

func (rf *Raft) killed() bool {
	z := atomic.LoadInt32(&rf.dead)
	return z == 1
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

	roler_string = map[int]string{
		LEADER:    "L",
		CANDIDATE: "C",
		FOLLOWER:  "F",
	}

	// set rand seed, copy code from
	// https: //pkg.go.dev/math/rand
	// rand.Seed(time.Now().UnixNano())

	num_servers := len(peers)

	rf := &Raft{}
	rf.cv = sync.NewCond(&rf.mu)

	rf.peers = peers
	rf.persister = persister
	rf.me = me

	rf.currentTerm = 0
	rf.votedFor = -1
	rf.log = []LogEntry{}
	// add dummy log
	rf.log = append(rf.log, LogEntry{})

	rf.roler = FOLLOWER

	rf.ResetElectionTimer()
	rf.AppendExpireTime = make([]time.Time, num_servers)
	for i := 0; i < num_servers; i++ {
		rf.ResetAppendTimer(i, false)
	}

	rf.nextIndex = make([]int, num_servers)
	rf.matchIndex = make([]int, num_servers)

	rf.applyQueue = make([]ApplyMsg, 0)
	// Your initialization code here (2A, 2B, 2C).

	// initialize from state persisted before a crash
	rf.readPersist(persister.ReadRaftState())

	// start ticker goroutine to start elections
	go rf.election_ticker()
	go rf.heartbeat_ticker()

	// start async apply goroutine
	go rf.Applier(applyCh)

	Debug(dInfo, "Start S%d", me)

	return rf
}
