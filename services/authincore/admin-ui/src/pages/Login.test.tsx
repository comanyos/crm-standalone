import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import Login, { getSafeReturnTo } from './Login'

const response = (body: unknown, status = 200) => new Response(JSON.stringify(body), {
  status, headers: { 'Content-Type': 'application/json' },
})

describe('admin authentication', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('submits login credentials using the password flow', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(response({ id: 'login-flow' }))
      .mockResolvedValueOnce(response({ error: 'expected test stop' }, 400))
    vi.stubGlobal('fetch', fetchMock)
    render(<MemoryRouter><Login /></MemoryRouter>)
    fireEvent.change(screen.getByLabelText('Email'), { target: { value: 'admin@example.test' } })
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'secure password' } })
    fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))
    await screen.findByText('expected test stop')
    const payload = JSON.parse(fetchMock.mock.calls[1][1].body)
    expect(payload).toEqual({ method: 'password', body: { identifier: 'admin@example.test', password: 'secure password' } })
  })

  it('uses registration traits rather than the login-only identifier shape', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(response({ ID: 'registration-flow' }))
      .mockResolvedValueOnce(response({ error: 'expected test stop' }, 400))
    vi.stubGlobal('fetch', fetchMock)
    render(<MemoryRouter><Login /></MemoryRouter>)
    fireEvent.click(screen.getByRole('button', { name: 'Register' }))
    fireEvent.change(screen.getByLabelText('Email'), { target: { value: 'new@example.test' } })
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'secure password' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create account' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    expect(JSON.parse(fetchMock.mock.calls[1][1].body)).toEqual({
      method: 'password', body: { traits: { email: 'new@example.test' }, password: 'secure password', schema_id: 'default' },
    })
  })
})

describe('login return target', () => {
  it('accepts the DBCore application URL', () => {
    expect(getSafeReturnTo('?return_to=http%3A%2F%2Flocalhost%3A9304%2Fdatabases')).toBe(
      'http://localhost:9304/databases',
    )
  })

  it('rejects external and executable return targets', () => {
    expect(getSafeReturnTo('?return_to=https%3A%2F%2Fevil.example%2F')).toBeNull()
    expect(getSafeReturnTo('?return_to=javascript%3Aalert(1)')).toBeNull()
  })
})
