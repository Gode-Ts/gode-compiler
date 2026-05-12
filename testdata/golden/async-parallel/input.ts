export type User = {
  id: string
}

export type Profile = {
  id: string
}

export type UserBundle = {
  user: User
  profile: Profile
}

declare function fetchUser(id: string): Promise<User>
declare function fetchProfile(id: string): Promise<Profile>

export async function loadBundle(id: string): Promise<UserBundle> {
  const user = await fetchUser(id)
  const profile = await fetchProfile(id)
  return { user, profile }
}
