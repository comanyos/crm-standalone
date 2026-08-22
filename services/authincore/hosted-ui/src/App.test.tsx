import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from './App'

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

const loginFlow = {
  ID: '11111111-1111-1111-1111-111111111111',
  UI: { Nodes: [
    { Type: 'input', Group: 'password', Attributes: { name: 'identifier', type: 'email', required: true } },
    { Type: 'input', Group: 'password', Attributes: { name: 'password', type: 'password', required: true } },
    { Type: 'input', Group: 'password', Attributes: { name: 'method', type: 'submit', value: 'password' } },
  ], Messages: [] },
}

describe('hosted authentication UI', () => {
  beforeEach(() => {
    history.replaceState({}, '', '/login')
  })
  afterEach(() => vi.unstubAllGlobals())

  it('renders and submits the password login journey', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse(loginFlow))
	  .mockResolvedValueOnce(jsonResponse(loginFlow, 422))
    vi.stubGlobal('fetch', fetchMock)
    render(<App />)
    fireEvent.change(await screen.findByLabelText('identifier'), { target: { value: 'user@example.test' } })
    fireEvent.change(screen.getByLabelText('password'), { target: { value: 'correct password' } })
    fireEvent.click(screen.getByRole('button', { name: 'Continue' }))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    const [, request] = fetchMock.mock.calls[1]
    expect(JSON.parse(request.body)).toEqual({
      method: 'password',
      body: { identifier: 'user@example.test', password: 'correct password' },
    })
  })

  it('nests registration traits in the API payload', async () => {
    history.replaceState({}, '', '/registration')
    const registrationFlow = {
      ID: '22222222-2222-2222-2222-222222222222',
      UI: { Nodes: [
        { Group: 'password', Attributes: { name: 'traits.email', type: 'email', required: true } },
        { Group: 'password', Attributes: { name: 'password', type: 'password', required: true } },
        { Group: 'password', Attributes: { name: 'method', type: 'submit', value: 'password' } },
      ] },
    }
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse(registrationFlow))
	  .mockResolvedValueOnce(jsonResponse(registrationFlow, 422))
    vi.stubGlobal('fetch', fetchMock)
    render(<App />)
    fireEvent.change(await screen.findByLabelText('email'), { target: { value: 'new@example.test' } })
    fireEvent.change(screen.getByLabelText('password'), { target: { value: 'long secure password' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create account' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    expect(JSON.parse(fetchMock.mock.calls[1][1].body).body).toEqual({
      traits: { email: 'new@example.test' }, password: 'long secure password', schema_id: 'default',
    })
  })

  it('accepts an invitation only for the server-bound email', async () => {
    history.replaceState({}, '', '/invitations/signed-token')
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ email: 'invited@example.test', role: 'member' }))
      .mockResolvedValueOnce(jsonResponse({ accepted: true }))
    vi.stubGlobal('fetch', fetchMock)
    render(<App />)
    expect(await screen.findByText('invited@example.test')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Accept invitation' }))
    await screen.findByText('Invitation accepted.')
    expect(JSON.parse(fetchMock.mock.calls[1][1].body)).toEqual({ email: 'invited@example.test' })
  })
})
