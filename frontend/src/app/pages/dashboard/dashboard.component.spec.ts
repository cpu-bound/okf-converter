import { signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { of, throwError } from 'rxjs';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { AuthService } from '../../services/auth.service';
import { FilesService, UserFile } from '../../services/files.service';
import { DashboardComponent } from './dashboard.component';

const POLL_INTERVAL_MS = 2500;
const MAX_POLL_MS = 10 * 60 * 1000;

function aFile(overrides: Partial<UserFile> = {}): UserFile {
  return {
    id: 'file-1',
    original_name: 'notas.md',
    content_type: 'text/markdown',
    size: 1024,
    status: 'converting',
    created_at: '2026-08-16T12:00:00Z',
    ...overrides
  };
}

describe('DashboardComponent', () => {
  // What files.list() will answer with on the next call. Reassigning it
  // between ticks is how a conversion "finishes" mid-test.
  let listed: UserFile[];
  let listCalls: number;
  let listFails: boolean;

  function build(): DashboardComponent {
    listCalls = 0;

    const files = {
      list: () => {
        listCalls++;
        return listFails
          ? throwError(() => new Error('boom'))
          : of(listed);
      }
    };

    TestBed.configureTestingModule({
      providers: [
        { provide: FilesService, useValue: files },
        { provide: AuthService, useValue: { user: signal(null) } }
      ]
    });

    // Built directly rather than through a fixture: none of what is being
    // tested here is rendering, and this keeps the test off the template.
    return TestBed.runInInjectionContext(() => new DashboardComponent());
  }

  beforeEach(() => {
    listed = [];
    listFails = false;
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
    TestBed.resetTestingModule();
  });

  it('carga la lista de archivos al iniciar', () => {
    listed = [aFile({ status: 'converted' })];

    const component = build();
    component.ngOnInit();

    expect(listCalls).toBe(1);
    expect(component.myFiles()).toHaveLength(1);

    component.ngOnDestroy();
  });

  // Nothing is waiting on a worker, so asking again would be pure noise.
  it('no consulta en bucle si no hay conversiones en curso', () => {
    listed = [aFile({ status: 'converted' }), aFile({ id: 'f2', status: 'failed' })];

    const component = build();
    component.ngOnInit();

    expect(component.polling()).toBe(false);

    vi.advanceTimersByTime(POLL_INTERVAL_MS * 4);
    expect(listCalls).toBe(1);

    component.ngOnDestroy();
  });

  // A file that was uploaded but never confirmed will never be picked up by
  // a worker, so it must not keep the dashboard polling forever.
  it('no consulta en bucle por una subida sin confirmar', () => {
    listed = [aFile({ status: 'pending' })];

    const component = build();
    component.ngOnInit();

    expect(component.polling()).toBe(false);

    component.ngOnDestroy();
  });

  it('consulta mientras haya una conversión en curso y para al terminar', () => {
    listed = [aFile({ status: 'converting' })];

    const component = build();
    component.ngOnInit();

    expect(component.polling()).toBe(true);

    vi.advanceTimersByTime(POLL_INTERVAL_MS);
    expect(listCalls).toBe(2);
    expect(component.polling()).toBe(true);

    // The worker finishes between one tick and the next.
    listed = [aFile({ status: 'converted', validation: 'valid' })];
    vi.advanceTimersByTime(POLL_INTERVAL_MS);

    expect(listCalls).toBe(3);
    expect(component.polling()).toBe(false);

    // And having stopped, it stays stopped.
    vi.advanceTimersByTime(POLL_INTERVAL_MS * 5);
    expect(listCalls).toBe(3);

    component.ngOnDestroy();
  });

  // A worker that died must not leave a browser tab asking forever.
  it('se rinde tras el tope de tiempo y lo dice', () => {
    listed = [aFile({ status: 'converting' })];

    const component = build();
    component.ngOnInit();

    vi.advanceTimersByTime(MAX_POLL_MS + POLL_INTERVAL_MS);

    expect(component.polling()).toBe(false);
    expect(component.pollingGaveUp()).toBe(true);

    const callsWhenItGaveUp = listCalls;
    vi.advanceTimersByTime(POLL_INTERVAL_MS * 5);
    expect(listCalls).toBe(callsWhenItGaveUp);

    component.ngOnDestroy();
  });

  // Polling on top of a failing request would just multiply the failures.
  it('deja de consultar si la lista falla', () => {
    listed = [aFile({ status: 'converting' })];
    listFails = true;

    const component = build();
    component.ngOnInit();

    expect(component.listError()).not.toBe('');
    expect(component.polling()).toBe(false);

    vi.advanceTimersByTime(POLL_INTERVAL_MS * 4);
    expect(listCalls).toBe(1);

    component.ngOnDestroy();
  });

  it('deja de consultar al destruir el componente', () => {
    listed = [aFile({ status: 'converting' })];

    const component = build();
    component.ngOnInit();
    component.ngOnDestroy();

    vi.advanceTimersByTime(POLL_INTERVAL_MS * 4);
    expect(listCalls).toBe(1);
  });

  // The gate the enunciado puts on the download: only a published bundle.
  it('solo ofrece la descarga cuando el bundle está publicado', () => {
    const component = build();

    expect(component.isDownloadable(aFile({ status: 'converted' }))).toBe(true);

    for (const status of ['pending', 'ready', 'converting', 'failed'] as const) {
      expect(component.isDownloadable(aFile({ status }))).toBe(false);
    }

    component.ngOnDestroy();
  });
});
