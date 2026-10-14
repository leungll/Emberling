import { RouterProvider } from 'react-router/dom';

import { router } from './routes';
import { ShortcutSheet } from '@/components/shortcuts/ShortcutSheet';
import { Toaster } from '@/components/ui/toast';

export function App() {
  return (
    <>
      <RouterProvider router={router} />
      <ShortcutSheet />
      <Toaster />
    </>
  );
}
