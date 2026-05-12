export type User = {
  id: string
  age: number
  active?: boolean
}

export function isAdult(user: User): boolean {
  return user.age >= 18
}
