import { create } from 'zustand';

/**
 * The operator credential the approval pages send with every request. It lives in memory
 * for this page only: it is never written to localStorage or sessionStorage, so closing or
 * reloading the tab forgets it, and no other page of the browser can read it.
 */
interface OperatorState {
  token: string;
  setToken: (token: string) => void;
  clearToken: () => void;
}

export const useOperatorStore = create<OperatorState>((set) => ({
  token: '',
  setToken: (token) => {
    set({ token: token.trim() });
  },
  clearToken: () => {
    set({ token: '' });
  },
}));
