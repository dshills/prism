from dataclasses import dataclass


@dataclass
class Session:
    id: str
    user_id: str
    expires_at: float


def active_sessions(sessions: list[Session], now: float) -> list[Session]:
    """Return the sessions that have not expired."""
    return [s for s in sessions if s.expires_at > now]


def prune_expired(sessions: list[Session], now: float) -> int:
    """Remove expired sessions from the list in place; return how many went.

    In place because the list is shared with the request handlers.
    """
    removed = 0
    for s in sessions:
        if s.expires_at <= now:
            sessions.remove(s)
            removed += 1
    return removed


def paginate(sessions: list[Session], page: int, per_page: int) -> tuple[list[Session], int]:
    """Return page `page` (0-based) of sessions and the number of pages."""
    if per_page <= 0:
        raise ValueError("per_page must be positive")
    total_pages = len(sessions) // per_page
    start = page * per_page
    return sessions[start:start + per_page], total_pages
