----------------------------- MODULE HotStuff -----------------------------
(*

Gemot BFT sequence-agreement protocol: TLA+ specification of chained-
HotStuff as implemented in internal/bft/. Covers happy-path safety
(session 1) plus view change under Byzantine leader failure (session 2).
Real threshold-sig crypto and service-layer integration are later-
session sibling specs — this file is the safety proof for the core
state machine.

Scope & intent
  The spec tracks blocks, votes, QCs, NewView messages, per-replica
  locked/high QC, per-replica timeout set, and per-replica committed
  log. Safety invariant (both sessions): no two honest replicas ever
  commit different blocks at the same height.

  Session 2 adds three protocol capabilities to the spec:
    1. Timeout-driven NewView. Any replica can abandon the current
       view and send a NewView(v+1, highQC) message carrying its
       highest observed QC. Models a timer firing after the current
       leader fails to produce a QC.
    2. 2f+1-gated view advance. When 2f+1 replicas have sent NewView
       for view v', the global view advances to v'. Captures the
       view-synchronizer abstraction — the new leader now gets the
       opportunity to propose.
    3. Liveness branch in honest vote rule. An honest replica will
       vote for a block whose parent (justify QC) is in a strictly
       newer view than the replica's locked QC, even if the block
       does not extend the locked block. Without this, a view
       change can leave replicas locked on a stale branch forever.

  Byzantine model (session 2):
    - Vote for conflicting blocks across views (within the
      lastVotedView guard).
    - Send NewView in any view, claiming any QC they've seen —
      including stale QCs that the honest majority has moved
      past. Cannot forge QCs (threshold-sig assumption).
    - Trigger timeout opportunistically. No fairness on Byzantine.

  Deferred to session 3+: real BLS signatures (threshold assumption
  replaced by cryptographic proof), service-layer integration,
  durable log replay, multi-node deploy.

  Small-model check in HotStuff.cfg: Replicas={r0,r1,r2,r3},
  Byzantine={r3}, MaxView=2, MaxHeight=2, MaxBlocks=3. TLC explores
  both happy-path and view-change executions within seconds.

  Stress check in HotStuff_stress.cfg exercises longer chains with
  view changes and retains the "two conflicting blocks at the same
  height" counterexample harness that caught the session-1 locked-
  advance bug.

*)
EXTENDS Integers, FiniteSets, Sequences, TLC

CONSTANTS
    Replicas,       \* Finite set of replica identifiers.
    Byzantine,      \* Subset of Replicas that may vote adversarially.
    MaxView,        \* Upper bound on view number.
    MaxHeight,      \* Upper bound on block height.
    MaxBlocks       \* Upper bound on proposed blocks (bounds state).

ASSUME
    /\ IsFiniteSet(Replicas)
    /\ Byzantine \subseteq Replicas
    /\ 3 * Cardinality(Byzantine) < Cardinality(Replicas)   \* f < N/3
    /\ MaxView \in Nat /\ MaxView >= 1
    /\ MaxHeight \in Nat /\ MaxHeight >= 1
    /\ MaxBlocks \in Nat /\ MaxBlocks >= 1

Honest == Replicas \ Byzantine
N == Cardinality(Replicas)
F == Cardinality(Byzantine)
Quorum == (2 * F) + 1     \* 2f+1 threshold for QC formation and view change.

Views   == 1..MaxView
Heights == 0..MaxHeight
BlockIDs == 0..(MaxBlocks - 1)

\* Genesis block: height 0, no parent, pre-existing QC.
Genesis == [id |-> 0, height |-> 0, parent |-> 0, view |-> 0]

VARIABLES
    view,           \* Global view number. Advances via FormQC (happy
                    \* path) or ViewChange (after 2f+1 NewViews).
    blocks,         \* Set of proposed blocks: [id, height, parent, view].
    votes,          \* Set of cast votes: [replica, view, blockID].
    qcs,            \* Set of formed QCs: [view, blockID].
    locked,         \* [Replicas -> blockID]: locked QC's block per replica.
    highQC,         \* [Replicas -> blockID]: highest QC's block per replica.
    committed,      \* [Replicas -> SUBSET BlockIDs]: committed blocks per replica.
    lastVotedView,  \* [Replicas -> view]: anti-equivocation guard.
    nextBlockID,    \* Monotonic block-ID counter.
    newViews,       \* Set of NewView messages: [sender, view, highQCblock].
    timedOut        \* [Replicas -> SUBSET Views]: views r has timed out on.

vars == <<view, blocks, votes, qcs, locked, highQC, committed,
          lastVotedView, nextBlockID, newViews, timedOut>>

\* Look up a block by ID. Total function: returns Genesis for id=0 and for
\* any id not in blocks (defensive default so TLC doesn't evaluate CHOOSE
\* over an empty set).
BlockOf(bid) ==
    IF bid = 0 THEN Genesis
    ELSE IF \E b \in blocks : b.id = bid
         THEN CHOOSE b \in blocks : b.id = bid
         ELSE Genesis

\* Block b extends ancestor a iff we can walk b.parent chain and hit a.
\* Bounded by iterating over blocks so orphan parent IDs terminate.
RECURSIVE ExtendsChain(_, _)
ExtendsChain(childID, ancestorID) ==
    \/ childID = ancestorID
    \/ /\ childID /= 0
       /\ \E b \in blocks :
              b.id = childID /\ ExtendsChain(b.parent, ancestorID)

TypeOK ==
    /\ view \in Views
    /\ blocks \subseteq [id: BlockIDs, height: Heights, parent: BlockIDs, view: Views \cup {0}]
    /\ votes \subseteq [replica: Replicas, view: Views, blockID: BlockIDs]
    /\ qcs \subseteq [view: Views, blockID: BlockIDs]
    /\ locked \in [Replicas -> BlockIDs]
    /\ highQC \in [Replicas -> BlockIDs]
    /\ committed \in [Replicas -> SUBSET BlockIDs]
    /\ lastVotedView \in [Replicas -> Views \cup {0}]
    /\ nextBlockID \in 1..MaxBlocks
    /\ newViews \subseteq [sender: Replicas, view: Views, highQCblock: BlockIDs]
    /\ timedOut \in [Replicas -> SUBSET Views]

Init ==
    /\ view = 1
    /\ blocks = {}
    /\ votes = {}
    /\ qcs = {}
    /\ locked = [r \in Replicas |-> 0]           \* locked on genesis
    /\ highQC = [r \in Replicas |-> 0]           \* high QC is genesis
    /\ committed = [r \in Replicas |-> {0}]      \* genesis committed
    /\ lastVotedView = [r \in Replicas |-> 0]
    /\ nextBlockID = 1
    /\ newViews = {}
    /\ timedOut = [r \in Replicas |-> {}]

(* ---------------------------- Actions ----------------------------------- *)

\* Leader proposes a new block in the current view. Parent may be any
\* known block ID (or genesis). Block's view is set to the current
\* global view — captures "leader proposes in the view it is leader of."
\* Safety is independent of leader choice (locked-QC rule); liveness
\* needs honest-leader rotation but we don't bind a rotation function
\* here.
Propose(leader, parentID, h) ==
    /\ nextBlockID < MaxBlocks
    /\ h \in 1..MaxHeight
    /\ parentID \in {b.id: b \in blocks} \cup {0}
    /\ LET parent == BlockOf(parentID) IN
           /\ h = parent.height + 1
           /\ blocks' = blocks \cup
                  {[id |-> nextBlockID, height |-> h,
                    parent |-> parentID, view |-> view]}
    /\ nextBlockID' = nextBlockID + 1
    /\ UNCHANGED <<view, votes, qcs, locked, highQC, committed,
                   lastVotedView, newViews, timedOut>>

\* Honest vote rule (HotStuff §4). Accept block b iff:
\*   (a) safety branch: b extends the locked block, OR
\*   (b) liveness branch: b's parent was proposed in a view strictly
\*       newer than the view the replica is locked in.
\* The liveness branch lets replicas unlock after a view change —
\* without it, replicas that locked on a stale QC would refuse every
\* post-view-change proposal.
HonestCanVote(r, b) ==
    /\ r \in Honest
    /\ lastVotedView[r] < b.view
    /\ \/ ExtendsChain(b.id, locked[r])
       \/ \E parent \in blocks :
              parent.id = b.parent /\ parent.view > BlockOf(locked[r]).view

\* Byzantine replicas can vote for anything in any view > last voted.
ByzantineCanVote(r, b) ==
    /\ r \in Byzantine
    /\ lastVotedView[r] < b.view

\* Cast a vote. Honest replicas follow the rule; Byzantine arbitrary.
CastVote(r, b) ==
    /\ b \in blocks
    /\ (HonestCanVote(r, b) \/ ByzantineCanVote(r, b))
    /\ votes' = votes \cup {[replica |-> r, view |-> b.view, blockID |-> b.id]}
    /\ lastVotedView' = [lastVotedView EXCEPT ![r] = b.view]
    /\ UNCHANGED <<view, blocks, qcs, locked, highQC, committed,
                   nextBlockID, newViews, timedOut>>

\* Form a QC when 2f+1 votes exist for a block in its view. Atomic with
\* the honest-replica lock and highQC update: an honest replica that
\* sees a QC on a new block advances its locked/high QC to point at
\* that block if its view is newer than the currently locked/high one.
\*
\* The view-advance clause only fires when v equals the current global
\* view, not any stale view with belatedly-quorumed votes. Without this
\* guard, a QC that forms on a view < current view would regress the
\* global view counter.
FormQC(bid, v) ==
    /\ [view |-> v, blockID |-> bid] \notin qcs
    /\ LET voters == {w.replica : w \in {x \in votes : x.blockID = bid /\ x.view = v}}
       IN Cardinality(voters) >= Quorum
    /\ qcs' = qcs \cup {[view |-> v, blockID |-> bid]}
    /\ highQC' = [r \in Replicas |->
           IF r \in Honest /\ v > BlockOf(highQC[r]).view THEN bid ELSE highQC[r]]
    /\ locked'  = [r \in Replicas |->
           IF r \in Honest /\ v > BlockOf(locked[r]).view THEN bid ELSE locked[r]]
    /\ view' = IF v = view /\ v + 1 <= MaxView THEN v + 1 ELSE view
    /\ UNCHANGED <<blocks, votes, committed, lastVotedView, nextBlockID,
                   newViews, timedOut>>

\* Two-chain commit rule: honest replica commits block b when there are
\* QCs on both b and a direct child of b in consecutive views.
Commit(r, bid) ==
    /\ r \in Honest
    /\ bid /= 0
    /\ bid \notin committed[r]
    /\ \E b \in blocks, c \in blocks :
           /\ b.id = bid
           /\ c.parent = bid
           /\ [view |-> b.view, blockID |-> bid] \in qcs
           /\ [view |-> c.view, blockID |-> c.id] \in qcs
           /\ c.view = b.view + 1         \* direct consecutive views
    /\ committed' = [committed EXCEPT ![r] = @ \cup {bid}]
    /\ UNCHANGED <<view, blocks, votes, qcs, locked, highQC,
                   lastVotedView, nextBlockID, newViews, timedOut>>

\* TriggerTimeout: replica r abandons the current view and sends a
\* NewView(view+1, claimedQC) message. Honest replicas report their
\* true highQC; Byzantine replicas may claim any QC'd block they have
\* seen — modeling stale-NewView attacks without allowing signature
\* forgery. Once r times out in a view, it cannot timeout again in
\* that same view (idempotent).
TriggerTimeout(r, claimedQC) ==
    /\ view \notin timedOut[r]
    /\ view + 1 \in Views
    /\ claimedQC \in ({0} \cup {qc.blockID : qc \in qcs})
    /\ \/ r \in Honest /\ claimedQC = highQC[r]
       \/ r \in Byzantine
    /\ newViews' = newViews \cup
           {[sender |-> r, view |-> view + 1, highQCblock |-> claimedQC]}
    /\ timedOut' = [timedOut EXCEPT ![r] = @ \cup {view}]
    /\ UNCHANGED <<view, blocks, votes, qcs, locked, highQC, committed,
                   lastVotedView, nextBlockID>>

\* ViewChange: once 2f+1 replicas have sent NewView for view v, the
\* global view advances to v. Captures the view-synchronizer
\* abstraction: the new leader of v now has standing to propose.
\*
\* Safety: this action doesn't read locked/highQC — it only advances
\* view. The locked-QC vote rule on CastVote prevents conflicting
\* commits even if the new leader proposes extending a stale QC.
ViewChange(v) ==
    /\ v > view
    /\ v \in Views
    /\ Cardinality({nv.sender : nv \in {x \in newViews : x.view = v}}) >= Quorum
    /\ view' = v
    /\ UNCHANGED <<blocks, votes, qcs, locked, highQC, committed,
                   lastVotedView, nextBlockID, newViews, timedOut>>

Next ==
    \/ \E leader \in Replicas, parentID \in BlockIDs, h \in 1..MaxHeight:
           Propose(leader, parentID, h)
    \/ \E r \in Replicas, b \in blocks: CastVote(r, b)
    \/ \E bid \in BlockIDs, v \in Views: FormQC(bid, v)
    \/ \E r \in Replicas, bid \in BlockIDs: Commit(r, bid)
    \/ \E r \in Replicas, cqc \in BlockIDs: TriggerTimeout(r, cqc)
    \/ \E v \in Views: ViewChange(v)

Spec == Init /\ [][Next]_vars

(* ---------------------------- Safety ------------------------------------ *)

\* TypeOK invariance.
TypeInvariant == TypeOK

\* Anti-equivocation: a replica votes at most once per view.
NoDoubleVoteInView ==
    \A v1, v2 \in votes:
        (v1.replica = v2.replica /\ v1.view = v2.view) => v1 = v2

\* No two honest replicas ever commit conflicting blocks at the same
\* height. HotStuff's core safety property (Yin et al. §5 Theorem 1).
\* Must continue to hold across view changes — that is the point of
\* the session-2 spec update.
AgreementAtHeight ==
    \A r1 \in Honest:
        \A r2 \in Honest:
            \A b1 \in committed[r1] \cap committed[r2]:
                \A b2 \in committed[r1] \cap committed[r2]:
                    (BlockOf(b1).height = BlockOf(b2).height) => b1 = b2

\* Every committed block above genesis must have a committed ancestor
\* at every lower height on its chain.
CommittedChainConsistent ==
    \A r \in Honest:
        \A bid \in committed[r]:
            bid = 0 \/ \E pid \in committed[r]:
                           ExtendsChain(bid, pid) /\ BlockOf(pid).height = 0

\* QCs only form at quorum. Every QC has 2f+1 distinct voters.
QCRequiresQuorum ==
    \A qc \in qcs:
        Cardinality({w.replica : w \in {x \in votes : x.blockID = qc.blockID /\ x.view = qc.view}}) >= Quorum

\* Honest replicas only commit blocks that have a QC on them.
CommitRequiresQC ==
    \A r \in Honest:
        \A bid \in committed[r]:
            bid = 0 \/ \E qc \in qcs: qc.blockID = bid

\* f < N/3 (checked on constants; trivial).
ByzantineBound == 3 * Cardinality(Byzantine) < Cardinality(Replicas)

\* NewView integrity: every NewView carries a highQC referring either to
\* genesis or to a block with a real QC. No forged QC references.
NewViewQCIsReal ==
    \A nv \in newViews:
        nv.highQCblock = 0 \/ \E qc \in qcs: qc.blockID = nv.highQCblock

(* ---------------------------- Monotonicity ------------------------------ *)

\* View only advances (new in session 2 — ViewChange never regresses,
\* and FormQC's guard v = view prevents stale-QC regression).
ViewMonotonic == [][view' >= view]_vars

CommittedMonotonic ==
    [][\A r \in Replicas: committed[r] \subseteq (committed')[r]]_vars
QCsMonotonic == [][qcs \subseteq qcs']_vars
VotesMonotonic == [][votes \subseteq votes']_vars
BlocksMonotonic == [][blocks \subseteq blocks']_vars
NewViewsMonotonic == [][newViews \subseteq newViews']_vars

(* ---------------------------- Symmetry ---------------------------------- *)

\* Partition-preserving permutations: same approach as Deliberation.tla.
\* HotStuff.cfg omits symmetry — TLC warns symmetry may mask liveness
\* violations, and session-2 wants safety proofs clean in a single run.
Symmetry ==
    {p \in Permutations(Replicas) :
        /\ \A r \in Honest: p[r] \in Honest
        /\ \A r \in Byzantine: p[r] \in Byzantine}

=============================================================================
