package raft

import "time"

func (rf *Raft) heartbeat_ticker() {
	for !rf.killed() {
		// Your code here to check if a leader election should
		// be started and to randomize sleeping time using
		// time.Sleep().
		time.Sleep(APPEND_TIMER_RESOLUTION * time.Millisecond)
		rf.mu.Lock()
		// check roler
		if rf.roler != LEADER {
			rf.mu.Unlock()
			continue
		}
		// check the append timer of every peers
		for i := 0; i < len(rf.peers); i++ {
			if i == rf.me {
				continue
			}
			if !time.Now().After(rf.AppendExpireTime[i]) {
				continue
			}
			if rf.nextIndex[i] <= rf.getFirstIndex() {
				// need to send installSnapshot RPC
				// TODO
			} else {
				// send appendEntry to peers_i
				logs := make([]LogEntry, rf.getLastIndex()+1-rf.nextIndex[i])
				copy(logs, rf.log[rf.nextIndex[i]-rf.getFirstIndex():])
				go rf.CallAppendEntries(i, rf.currentTerm, rf.me, rf.nextIndex[i]-1, rf.getTermByIndex(rf.nextIndex[i]-1), logs, rf.commitIdx)
			}
			rf.ResetAppendTimer(i, false)
		}
		rf.mu.Unlock()
	}
}

func (rf *Raft) CallAppendEntries(idx int, term int, me int, prevLogIndex int, prevLogTerm int, logs []LogEntry, leaderCommit int) {

	args := AppendEntriesArgs{}
	reply := AppendEntriesReply{}
	args.Term = term
	args.LeaderId = me
	args.PrevLogIndex = prevLogIndex
	args.PrevLogTerm = prevLogTerm
	args.Entries = logs
	args.LeaderCommit = leaderCommit

	Debug(dLog, "S%d T%d Send AppendEntries to S%d, Args is %v", me, term, idx, args)

	ok := rf.sendAppendEntries(idx, &args, &reply)
	if ok {
		rf.mu.Lock()
		defer rf.mu.Unlock()

		// check term replyTerm and curTerm
		if reply.Term < rf.currentTerm {
			return
		}
		if reply.Term > rf.currentTerm {
			rf.changeToFollower(reply.Term)
			rf.ResetElectionTimer()
			return
		}

		// check roler
		if rf.roler != LEADER {
			return
		}
		// check argsTerm and curTerm
		// something will happen. when this leader sent x in term i
		// then this leader become leader of term i + 2
		// other server at term i + 2 receive this message
		// and reply with false, but it is a outof date reply
		if args.Term != rf.currentTerm {
			return
		}
		// check rf.nextIndex and args.PrevLogIndex to filter out of date reply
		if rf.nextIndex[idx] != args.PrevLogIndex+1 {
			return
		}

		// when reach here, the following condition is ok:
		// reply.Term == rf.curTerm == args.Term
		// rf.nextIndex[idx] == args.PrevLogIndex + 1
		// means this is not a out of date reply
		if reply.Success {
			// update the nextIndex and matchIndex
			rf.nextIndex[idx] = rf.nextIndex[idx] + len(logs)
			rf.matchIndex[idx] = rf.nextIndex[idx] - 1

			// check whether can update commitIndex
			diff := make([]int, rf.getLastIndex()+5)
			for i := 0; i < len(rf.peers); i++ {
				if i == rf.me {
					continue
				}
				diff[0] += 1
				diff[rf.matchIndex[i]+1] -= 1
			}
			ok_idx := 0
			for i := 1; i < len(diff); i++ {
				diff[i] += diff[i-1]
				if diff[i]+1 > len(rf.peers)/2 {
					ok_idx = i
				}
			}

			if ok_idx > rf.commitIdx && rf.log[ok_idx].Term == rf.currentTerm {
				// if there is new commit Log, notify the applier to send
				Debug(dLog, "S%d at T%d Reset it CI From %d to %d", rf.me, rf.currentTerm, rf.commitIdx, ok_idx)
				for i := rf.commitIdx + 1; i <= ok_idx; i++ {
					rf.applyQueue = append(rf.applyQueue, ApplyMsg{
						CommandValid: true,
						CommandIndex: i,
						Command:      rf.getCommandByIndex(i),
					})
				}
				rf.commitIdx = ok_idx
				rf.cv.Broadcast()
			}
		} else {
			// optimized method
			// from https://thesquareplanet.com/blog/students-guide-to-raft/
			if reply.ConflictTerm == -1 {
				rf.nextIndex[idx] = reply.ConflictIndex
			} else {
				findIdx := -1
				for i := rf.getLastIndex() + 1; i > rf.getFirstIndex(); i-- {
					if rf.getTermByIndex(i-1) == reply.ConflictTerm {
						findIdx = i
						break
					}
				}
				if findIdx != -1 {
					// if find a log of conflict term,
					// set to the one beyond the index
					rf.nextIndex[idx] = findIdx
				} else {
					// set the nextIdx to the conflict firstLog index of peer's logs
					rf.nextIndex[idx] = reply.ConflictIndex
				}
			}
		}

		// whether suc of not, if the nextIndex[idx] is not the len(rf.log)
		// means this follower log is not matched, seend appendEntry to this
		// peer immediately
		if rf.nextIndex[idx] != rf.getLastIndex()+1 {
			rf.ResetAppendTimer(idx, true)
		}
	}
}
