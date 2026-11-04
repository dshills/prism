from dataclasses import dataclass


@dataclass
class Session:
    id: str
    user_id: str
    expires_at: float


def active_sessions(sessions: list[Session], now: float) -> list[Session]:
    """Return the sessions that have not expired."""
    return [s for s in sessions if s.expires_at > now]
