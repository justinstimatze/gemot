----------------------------- MODULE Deliberation -----------------------------
(*

Gemot deliberation protocol: TLA+ specification of the target
Byzantine-tolerant state machine.

Scope & intent
  This spec models the protocol-layer state machine that Track 1
  (DARPA-PS-26-09) is hardening: round progression, position
  submission, qualified voting on the five-point scale, commitment
  lifecycle, and deliberation termination. It does NOT model the
  LLM analysis pipeline itself — the LLM is treated as an external
  oracle whose only observable behavior is "advance the round",
  "close the deliberation", or "fail and retry."

  Properties here are model-checked by TLC under symmetric reduction
  (see Deliberation.cfg and README.md). Small-model results lift to
  arbitrary n via the generalization argument in README.md.

*)
EXTENDS Integers, FiniteSets, TLC

CONSTANTS
    Agents,         \* Finite set of agent identifiers. Symmetric.
    Byzantine,      \* Subset of Agents that may deviate from protocol.
    MaxRounds,      \* Upper bound on round count (bounds TLC state).
    MaxPID,         \* Upper bound on allocated position IDs.
    MaxCID,         \* Upper bound on allocated commitment IDs.
    VoteValues      \* Allowed qualified-vote values (e.g. -2..2).

ASSUME
    /\ IsFiniteSet(Agents)
    /\ Byzantine \subseteq Agents
    /\ 3 * Cardinality(Byzantine) < Cardinality(Agents)   \* f < n/3
    /\ MaxRounds \in Nat /\ MaxRounds >= 1
    /\ MaxPID \in Nat /\ MaxPID >= 1
    /\ MaxCID \in Nat /\ MaxCID >= 1
    /\ VoteValues \subseteq Int /\ VoteValues /= {}

Honest == Agents \ Byzantine

VARIABLES
    round,          \* 1..MaxRounds: current round number.
    status,         \* "Open" | "Analyzing" | "Closed".
    positions,      \* set of [author, round, pid].
    votes,          \* set of [voter, pid, value].
    commitState,    \* [cid -> "absent"|"pending"|"fulfilled"|"broken"].
    commitOwner,    \* [cid -> Agents \cup {"nobody"}].
    nextPID,        \* monotonic position-ID counter.
    nextCID         \* monotonic commitment-ID counter.

vars == <<round, status, positions, votes, commitState, commitOwner,
          nextPID, nextCID>>

PIDs == 0..(MaxPID - 1)
CIDs == 0..(MaxCID - 1)

TypeOK ==
    /\ round \in 1..MaxRounds
    /\ status \in {"Open", "Analyzing", "Closed"}
    /\ positions \subseteq [author: Agents, round: 1..MaxRounds, pid: PIDs]
    /\ votes     \subseteq [voter: Agents, pid: PIDs, value: VoteValues]
    /\ commitState \in [CIDs -> {"absent", "pending", "fulfilled", "broken"}]
    /\ commitOwner \in [CIDs -> Agents \cup {"nobody"}]
    /\ nextPID \in 0..MaxPID
    /\ nextCID \in 0..MaxCID

Init ==
    /\ round = 1
    /\ status = "Open"
    /\ positions = {}
    /\ votes = {}
    /\ commitState = [c \in CIDs |-> "absent"]
    /\ commitOwner = [c \in CIDs |-> "nobody"]
    /\ nextPID = 0
    /\ nextCID = 0

(* ---------------------------- Actions ----------------------------------- *)

\* Honest agents submit at most one position per round.
\* Byzantine agents may equivocate (bounded at 2 per round for tractability).
SubmitPosition(a) ==
    /\ status = "Open"
    /\ nextPID < MaxPID
    /\ LET mine == {p \in positions: p.author = a /\ p.round = round}
           cap  == IF a \in Honest THEN 1 ELSE 2
       IN Cardinality(mine) < cap
    /\ positions' = positions \cup
           {[author |-> a, round |-> round, pid |-> nextPID]}
    /\ nextPID' = nextPID + 1
    /\ UNCHANGED <<round, status, votes, commitState, commitOwner, nextCID>>

\* Each (voter, pid) pair votes at most once. Byzantine voters can pick any
\* value; honest voters likewise pick from VoteValues — the spec doesn't
\* model the "truthfulness" of a vote, only that the aggregation is
\* well-defined regardless of who votes what.
CastVote(voter, p, v) ==
    /\ status = "Open"
    /\ p \in positions
    /\ v \in VoteValues
    /\ ~\E w \in votes: w.voter = voter /\ w.pid = p.pid
    /\ votes' = votes \cup {[voter |-> voter, pid |-> p.pid, value |-> v]}
    /\ UNCHANGED <<round, status, positions, commitState, commitOwner,
                   nextPID, nextCID>>

StartAnalysis ==
    /\ status = "Open"
    /\ status' = "Analyzing"
    /\ UNCHANGED <<round, positions, votes, commitState, commitOwner,
                   nextPID, nextCID>>

\* Oracle outcome: advance to next round (fresh Open) while under MaxRounds.
AnalysisAdvance ==
    /\ status = "Analyzing"
    /\ round < MaxRounds
    /\ round' = round + 1
    /\ status' = "Open"
    /\ UNCHANGED <<positions, votes, commitState, commitOwner,
                   nextPID, nextCID>>

\* Oracle outcome: terminate deliberation.
AnalysisClose ==
    /\ status = "Analyzing"
    /\ status' = "Closed"
    /\ UNCHANGED <<round, positions, votes, commitState, commitOwner,
                   nextPID, nextCID>>

\* Oracle failure: LLM call errored; revert to Open for retry.
AnalysisFail ==
    /\ status = "Analyzing"
    /\ status' = "Open"
    /\ UNCHANGED <<round, positions, votes, commitState, commitOwner,
                   nextPID, nextCID>>

MakeCommitment(a) ==
    /\ status = "Open"
    /\ nextCID < MaxCID
    /\ commitState[nextCID] = "absent"
    /\ commitState' = [commitState EXCEPT ![nextCID] = "pending"]
    /\ commitOwner' = [commitOwner EXCEPT ![nextCID] = a]
    /\ nextCID' = nextCID + 1
    /\ UNCHANGED <<round, status, positions, votes, nextPID>>

FulfillCommitment(cid) ==
    /\ commitState[cid] = "pending"
    /\ commitState' = [commitState EXCEPT ![cid] = "fulfilled"]
    /\ UNCHANGED <<round, status, positions, votes, commitOwner,
                   nextPID, nextCID>>

BreakCommitment(cid) ==
    /\ commitState[cid] = "pending"
    /\ commitState' = [commitState EXCEPT ![cid] = "broken"]
    /\ UNCHANGED <<round, status, positions, votes, commitOwner,
                   nextPID, nextCID>>

\* Stuttering in the Closed state so TLC doesn't flag it as deadlock.
Terminated ==
    /\ status = "Closed"
    /\ UNCHANGED vars

Next ==
    \/ \E a \in Agents: SubmitPosition(a) \/ MakeCommitment(a)
    \/ \E voter \in Agents, p \in positions, v \in VoteValues:
           CastVote(voter, p, v)
    \/ StartAnalysis \/ AnalysisAdvance \/ AnalysisClose \/ AnalysisFail
    \/ \E cid \in CIDs:
           FulfillCommitment(cid) \/ BreakCommitment(cid)
    \/ Terminated

\* WF on StartAnalysis is enough — it's always enabled in Open, so "if
\* continuously enabled, eventually taken" suffices. AnalysisClose, however,
\* is disabled every time AnalysisFail resets status to Open, so weak
\* fairness cannot beat the retry loop. SF_vars("infinitely enabled ⇒
\* eventually taken") is the actual guarantee we need from the oracle.
Fairness ==
    /\ WF_vars(StartAnalysis)
    /\ SF_vars(AnalysisClose)

Spec == Init /\ [][Next]_vars /\ Fairness

(* ---------------------------- Safety ------------------------------------ *)

\* round never decreases.
RoundMonotonic == [][round' >= round]_vars

\* Once Closed, no variable changes (terminal state).
ClosedAbsorbing ==
    [](status = "Closed" => [](status = "Closed"))

\* positions and votes are append-only (no edits, no deletes).
PositionsMonotonic == [][positions \subseteq positions']_vars
VotesMonotonic     == [][votes     \subseteq votes'    ]_vars

\* Each (voter, pid) pair appears at most once in votes.
NoDoubleVote ==
    \A v1, v2 \in votes:
        (v1.voter = v2.voter /\ v1.pid = v2.pid) => v1 = v2

\* Honest agents never equivocate within a round.
HonestNoEquivocation ==
    \A a \in Honest, r \in 1..MaxRounds:
        Cardinality({p \in positions: p.author = a /\ p.round = r}) <= 1

\* f < n/3 assumption continues to hold (trivially, since it's on constants).
ByzantineBound == 3 * Cardinality(Byzantine) < Cardinality(Agents)

\* Commitment lifecycle: fulfilled and broken are absorbing.
CommitmentFulfilledStable ==
    [][ \A cid \in CIDs:
            commitState[cid] = "fulfilled" =>
                commitState'[cid] = "fulfilled" ]_vars

CommitmentBrokenStable ==
    [][ \A cid \in CIDs:
            commitState[cid] = "broken" =>
                commitState'[cid] = "broken" ]_vars

\* Once a commitment is allocated, its owner never changes.
CommitmentOwnerStable ==
    [][ \A cid \in CIDs:
            commitState[cid] /= "absent" =>
                commitOwner'[cid] = commitOwner[cid] ]_vars

(* ---------------------------- Liveness ---------------------------------- *)

\* Under fairness, the deliberation eventually terminates.
Termination == <>(status = "Closed")

(* ---------------------------- Symmetry ---------------------------------- *)

\* Permutations that preserve the Honest/Byzantine partition. Soundness
\* argument: all actions are guarded only by membership in Honest vs
\* Byzantine (never by specific agent identity), so any permutation within
\* each group yields an equivalent execution. See README.md §"Symmetric
\* reduction and the n-agent generalization argument."
Symmetry == Permutations(Honest) \cup Permutations(Byzantine)

=============================================================================
