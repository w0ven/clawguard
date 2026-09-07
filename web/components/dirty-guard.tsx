"use client";

import type { ReactNode } from "react";
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";

type DirtyOwnerId = symbol;
type DirtyOwner = {
  message: string;
  scope?: string;
};
type HistoryState = Record<string, unknown>;

type DirtyGuardContextValue = {
  isDirty: boolean;
  registerDirtyOwner: (
    owner: DirtyOwnerId,
    dirty: boolean,
    message: string,
    scope?: string,
  ) => void;
  confirmNavigation: (message?: string, ownerScope?: string) => boolean;
};

const DEFAULT_MESSAGE = "当前页面有未保存的修改，确定离开吗？";
const SENTINEL_KEY = "__clawguard_dirty_guard";
const RESTORE_TIMEOUT_MS = 1000;
const ACCEPT_TIMEOUT_MS = 2000;

const DirtyGuardContext = createContext<DirtyGuardContextValue>({
  isDirty: false,
  registerDirtyOwner: () => undefined,
  confirmNavigation: () => true,
});

function isHistoryState(value: unknown): value is HistoryState {
  return Boolean(value && typeof value === "object" && !Array.isArray(value));
}

function isSentinelState(value: unknown) {
  return isHistoryState(value) && value[SENTINEL_KEY] === true;
}

function makeSentinelState(value: unknown): HistoryState {
  const next = isHistoryState(value) ? { ...value } : {};
  next[SENTINEL_KEY] = true;
  return next;
}

type ProtectedEntry = {
  href: string;
  state: HistoryState;
};

type RestoreAttempt = ProtectedEntry & {
  timer: number;
};

type AcceptedTraversal = {
  timer: number;
};

type AcceptedForward = {
  href: string;
  timer: number;
};

type AcceptedLeave = {
  href: string;
  timer: number;
};

export function DirtyGuardProvider({ children }: { children: ReactNode }) {
  const ownersRef = useRef(new Map<DirtyOwnerId, DirtyOwner>());
  const dirtyRef = useRef(false);
  const [isDirty, setIsDirty] = useState(false);
  const confirmNavigation = useCallback(
    (message?: string, ownerScope?: string) => {
      if (!dirtyRef.current) {
        return true;
      }
      const owners = [...ownersRef.current.values()];
      const selectedOwner = ownerScope
        ? owners.find((owner) => owner.scope === ownerScope)
        : owners[0];
      if (ownerScope && !selectedOwner) {
        return true;
      }
      return window.confirm(
        message ?? selectedOwner?.message ?? DEFAULT_MESSAGE,
      );
    },
    [],
  );
  const registerDirtyOwner = useCallback(
    (
      owner: DirtyOwnerId,
      dirty: boolean,
      message: string,
      scope?: string,
    ) => {
      if (dirty) {
        ownersRef.current.set(owner, { message, scope });
      } else {
        ownersRef.current.delete(owner);
      }
      const nextDirty = ownersRef.current.size > 0;
      dirtyRef.current = nextDirty;
      setIsDirty((current) => (current === nextDirty ? current : nextDirty));
    },
    [],
  );
  const protectedEntryRef = useRef<ProtectedEntry | null>(null);
  const restoreAttemptRef = useRef<RestoreAttempt | null>(null);
  const acceptedTraversalRef = useRef<AcceptedTraversal | null>(null);
  const acceptedForwardRef = useRef<AcceptedForward | null>(null);
  const acceptedLeaveRef = useRef<AcceptedLeave | null>(null);
  const restoredSinceDirtyRef = useRef(false);
  const guardedHrefRef = useRef<string | null>(null);

  const clearRestoreAttempt = useCallback(() => {
    const attempt = restoreAttemptRef.current;
    if (attempt) {
      window.clearTimeout(attempt.timer);
      restoreAttemptRef.current = null;
    }
  }, []);

  const clearAcceptedTraversal = useCallback(() => {
    const traversal = acceptedTraversalRef.current;
    if (traversal) {
      window.clearTimeout(traversal.timer);
      acceptedTraversalRef.current = null;
    }
  }, []);

  const clearAcceptedForward = useCallback(() => {
    const forward = acceptedForwardRef.current;
    if (forward) {
      window.clearTimeout(forward.timer);
      acceptedForwardRef.current = null;
    }
  }, []);

  const clearAcceptedLeave = useCallback(() => {
    const leave = acceptedLeaveRef.current;
    if (leave) {
      window.clearTimeout(leave.timer);
      acceptedLeaveRef.current = null;
    }
  }, []);

  useEffect(() => {
    if (!isDirty) {
      guardedHrefRef.current = null;
      protectedEntryRef.current = null;
      restoredSinceDirtyRef.current = false;
      clearRestoreAttempt();
      clearAcceptedTraversal();
      clearAcceptedForward();
      clearAcceptedLeave();
      return;
    }

    const href = window.location.href;
    const currentState: unknown = window.history.state;
    const activationChanged = guardedHrefRef.current !== href;
    const shouldPushSentinel = activationChanged || isSentinelState(currentState) === false;
    const state = makeSentinelState(currentState);
    if (shouldPushSentinel) {
      // Keep Next's existing history state and append one same-URL entry.
      // The duplicate is the return target when a dirty back gesture is
      // cancelled; it also guarantees history.forward() has a real target.
      guardedHrefRef.current = href;
      try {
        window.history.pushState(state, "", href);
      } catch {
        // The popstate handler still has a URL fallback if the browser refuses
        // to add the same-document sentinel.
      }
    }
    protectedEntryRef.current = { href, state };
  }, [
    clearAcceptedForward,
    clearAcceptedLeave,
    clearAcceptedTraversal,
    clearRestoreAttempt,
    isDirty,
  ]);

  useEffect(() => {
    const beforeUnload = (event: BeforeUnloadEvent) => {
      if (acceptedTraversalRef.current) {
        // The accepted same-URL sentinel is followed by one real traversal.
        // Do not ask a second time when that traversal unloads the document.
        clearAcceptedTraversal();
        return;
      }
      if (!dirtyRef.current) {
        return;
      }
      event.preventDefault();
      event.returnValue = DEFAULT_MESSAGE;
    };

    const handlePopState = (event: PopStateEvent) => {
      const acceptedTraversal = acceptedTraversalRef.current;
      if (acceptedTraversal) {
        clearAcceptedTraversal();
        // This is the real destination traversal after an accepted sentinel.
        // Next must receive this event so its route state remains authoritative.
        return;
      }

      const acceptedForward = acceptedForwardRef.current;
      if (acceptedForward) {
        if (window.location.href === acceptedForward.href) {
          clearAcceptedForward();
          // Forward into the entry that was just accepted is clean and must not
          // ask the same confirmation again. Let Next process the route.
          return;
        }
        clearAcceptedForward();
      }

      let returningFromAcceptedLeave = false;
      const acceptedLeave = acceptedLeaveRef.current;
      if (acceptedLeave) {
        if (window.location.href === acceptedLeave.href) {
          clearAcceptedLeave();
          // A previous rejected traversal intentionally kept the forward
          // confirmation contract. If the user accepts that forward, consume
          // the current traversal; it is not another sentinel back.
          returningFromAcceptedLeave = true;
        } else {
          clearAcceptedLeave();
        }
      }

      const restoring = restoreAttemptRef.current;
      if (restoring) {
        // Next may rewrite history.state while preserving the URL. The
        // protected same-URL entry is still unambiguous during this one
        // restoration attempt, so use the URL as the final comparison.
        const returnedToProtectedEntry = window.location.href === restoring.href;
        if (returnedToProtectedEntry) {
          clearRestoreAttempt();
          restoredSinceDirtyRef.current = true;
          // Next's own popstate listener must not turn the restoration into a
          // second route transition or unmount the draft tree.
          event.stopImmediatePropagation();
          return;
        }
        clearRestoreAttempt();
      }

      if (!dirtyRef.current) {
        return;
      }

      if (confirmNavigation()) {
        if (returningFromAcceptedLeave) {
          return;
        }
        const protectedEntry = protectedEntryRef.current;
        const atProtectedEntry =
          protectedEntry && window.location.href === protectedEntry.href;
        if (!atProtectedEntry) {
          // There was no same-URL sentinel to consume (for example, history
          // insertion was refused), so the current traversal is already the
          // real accepted navigation.
          return;
        }

        // A rejected traversal may have restored the sentinel immediately
        // before this one. Keep that established forward-cancel contract; on
        // a fresh accepted traversal, allow one clean forward return instead.
        const hadPreviousRestore = restoredSinceDirtyRef.current;
        restoredSinceDirtyRef.current = false;
        clearAcceptedForward();
        clearAcceptedLeave();
        if (!hadPreviousRestore) {
          const forward: AcceptedForward = {
            href: protectedEntry.href,
            timer: 0,
          };
          acceptedForwardRef.current = forward;
          forward.timer = window.setTimeout(() => {
            if (acceptedForwardRef.current === forward) {
              acceptedForwardRef.current = null;
            }
          }, ACCEPT_TIMEOUT_MS);
        } else {
          const leave: AcceptedLeave = {
            href: protectedEntry.href,
            timer: 0,
          };
          acceptedLeaveRef.current = leave;
          leave.timer = window.setTimeout(() => {
            if (acceptedLeaveRef.current === leave) {
              acceptedLeaveRef.current = null;
            }
          }, ACCEPT_TIMEOUT_MS);
        }

        // The first back only crosses the same-URL sentinel. Stop Next from
        // treating that no-op traversal as a route change, then cross the
        // actual predecessor entry exactly once.
        event.preventDefault();
        event.stopImmediatePropagation();
        const traversal: AcceptedTraversal = { timer: 0 };
        acceptedTraversalRef.current = traversal;
        traversal.timer = window.setTimeout(() => {
          if (acceptedTraversalRef.current === traversal) {
            acceptedTraversalRef.current = null;
          }
        }, ACCEPT_TIMEOUT_MS);
        window.setTimeout(() => {
          if (acceptedTraversalRef.current !== traversal) {
            return;
          }
          try {
            window.history.go(-1);
          } catch {
            window.history.back();
          }
        }, 0);
        return;
      }

      // popstate cannot be preventDefault'ed, but capture + immediate stop
      // prevents Next's router listener from consuming the rejected entry.
      event.preventDefault();
      event.stopImmediatePropagation();
      const protectedEntry = protectedEntryRef.current;
      if (!protectedEntry) {
        return;
      }

      restoredSinceDirtyRef.current = true;
      const attempt: RestoreAttempt = {
        ...protectedEntry,
        timer: 0,
      };
      restoreAttemptRef.current = attempt;
      try {
        window.history.forward();
      } catch {
        window.history.pushState(attempt.state, "", attempt.href);
        clearRestoreAttempt();
        return;
      }

      attempt.timer = window.setTimeout(() => {
        if (restoreAttemptRef.current !== attempt) {
          return;
        }
        // A forward traversal normally emits the restoration popstate above.
        // If there was no traversable forward entry, restore the URL directly
        // and clear the flag so the next real back gesture is still guarded.
        if (window.location.href !== attempt.href) {
          window.history.pushState(attempt.state, "", attempt.href);
        }
        restoreAttemptRef.current = null;
      }, RESTORE_TIMEOUT_MS);
    };

    window.addEventListener("beforeunload", beforeUnload);
    // Capture runs before Next App Router's normal popstate listener. The
    // listener is intentionally global: multiple editors share one confirm.
    window.addEventListener("popstate", handlePopState, true);
    return () => {
      window.removeEventListener("beforeunload", beforeUnload);
      window.removeEventListener("popstate", handlePopState, true);
      clearRestoreAttempt();
      clearAcceptedTraversal();
      clearAcceptedForward();
      clearAcceptedLeave();
    };
  }, [
    clearAcceptedForward,
    clearAcceptedLeave,
    clearAcceptedTraversal,
    clearRestoreAttempt,
    confirmNavigation,
  ]);

  const value = useMemo(
    () => ({ isDirty, registerDirtyOwner, confirmNavigation }),
    [confirmNavigation, isDirty, registerDirtyOwner],
  );

  return <DirtyGuardContext.Provider value={value}>{children}</DirtyGuardContext.Provider>;
}

export function useDirtyNavigation() {
  return useContext(DirtyGuardContext);
}

/**
 * Registers one editor's draft state. Each hook owns an independent registry
 * entry, so an editor unmounting cannot clear another editor's protection.
 * Internal links use the context confirm function; browser history is handled
 * once by DirtyGuardProvider.
 */
export function useDirtyGuard(
  dirty: boolean,
  message = DEFAULT_MESSAGE,
  scope?: string,
) {
  const { registerDirtyOwner } = useDirtyNavigation();
  const ownerId = useRef(Symbol());
  const dirtyRef = useRef(dirty);
  dirtyRef.current = dirty;

  useEffect(() => {
    registerDirtyOwner(ownerId.current, dirty, message, scope);
    return () => registerDirtyOwner(ownerId.current, false, message, scope);
  }, [dirty, message, registerDirtyOwner, scope]);

  return useCallback(
    (customMessage = message) =>
      !dirtyRef.current || window.confirm(customMessage),
    [message],
  );
}
