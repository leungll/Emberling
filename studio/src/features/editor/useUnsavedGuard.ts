import { useEffect } from 'react';
import { useBlocker, type Blocker } from 'react-router';

/**
 * Every way out of the Editor with unsaved changes asks first: the back button, the Runs
 * tab, a run link and browser Back are held by the router's blocker until the viewer
 * chooses; closing or reloading the tab uses the browser's own prompt. A change of the
 * query string alone stays on the page and is never held.
 *
 * `isDirty` is read at the moment of navigation, so a Save that has just finished lets the
 * navigation it starts through without waiting for a render.
 */
export function useUnsavedGuard(isDirty: () => boolean, dirty: boolean): Blocker {
  const blocker = useBlocker(
    ({ currentLocation, nextLocation }) =>
      isDirty() && currentLocation.pathname !== nextLocation.pathname,
  );

  useEffect(() => {
    if (!dirty) return;
    const onBeforeUnload = (e: BeforeUnloadEvent) => {
      e.preventDefault();
    };
    window.addEventListener('beforeunload', onBeforeUnload);
    return () => {
      window.removeEventListener('beforeunload', onBeforeUnload);
    };
  }, [dirty]);

  return blocker;
}
