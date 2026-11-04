import { Api, Store, User } from "./types";

export interface SyncResult {
  updated: number;
  failed: string[];
}

function normalize(user: User): User {
  return { ...user, email: user.email.trim().toLowerCase() };
}

// syncUsers copies every user into the store.
export async function syncUsers(api: Api, store: Store): Promise<SyncResult> {
  const users = await api.listUsers();
  const result: SyncResult = { updated: 0, failed: [] };
  for (const user of users) {
    try {
      await store.upsert(user.id, normalize(user));
      result.updated++;
    } catch (err) {
      result.failed.push(user.id);
    }
  }
  await store.setLastSync(new Date());
  return result;
}
