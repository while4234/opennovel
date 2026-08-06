import React from 'react';
import { createRoot } from 'react-dom/client';
import { createBrowserRouter, RouterProvider } from 'react-router-dom';
import App from './App.jsx';
import { NavigationGuardProvider } from './navigation/NavigationGuard.jsx';
import './styles.css';
import './workspace/workspace.css';

const router = createBrowserRouter([{
  path: '*',
  element: (
    <NavigationGuardProvider>
      <App />
    </NavigationGuardProvider>
  )
}]);

createRoot(document.getElementById('root')).render(
  <React.StrictMode>
    <RouterProvider router={router} />
  </React.StrictMode>
);
