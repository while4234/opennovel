import { createContext, useCallback, useContext, useEffect, useMemo, useRef } from 'react';
import { useBlocker, useNavigate } from 'react-router-dom';

const NavigationGuardContext = createContext(null);

export function NavigationGuardProvider({ children }) {
  const guardRef = useRef(null);
  const blocker = useBlocker(() => guardRef.current?.enabled === true);

  const register = useCallback((guard) => {
    guardRef.current = guard;
    return () => {
      if (guardRef.current === guard) guardRef.current = null;
    };
  }, []);

  useEffect(() => {
    if (blocker.state !== 'blocked') return;
    const guard = guardRef.current;
    if (!guard?.enabled || guard.confirm('navigation')) {
      blocker.proceed();
      return;
    }
    blocker.reset();
  }, [blocker]);

  useEffect(() => {
    const handleBeforeUnload = (event) => {
      if (!guardRef.current?.enabled) return;
      event.preventDefault();
      event.returnValue = '';
    };
    window.addEventListener('beforeunload', handleBeforeUnload);
    return () => window.removeEventListener('beforeunload', handleBeforeUnload);
  }, []);

  const value = useMemo(() => ({ register }), [register]);
  return <NavigationGuardContext.Provider value={value}>{children}</NavigationGuardContext.Provider>;
}

export function useNavigationGuard(enabled, confirm) {
  const context = useContext(NavigationGuardContext);
  useEffect(() => {
    if (!context) return undefined;
    return context.register({ enabled, confirm });
  }, [confirm, context, enabled]);
}

export function useGuardedNavigate() {
  const navigate = useNavigate();
  return useCallback((to, options) => {
    navigate(to, options);
    return true;
  }, [navigate]);
}
