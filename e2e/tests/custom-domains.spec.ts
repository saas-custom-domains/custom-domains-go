import { expect, test } from '@playwright/test'

// Customer domains go through the mock proxy (see playwright.config.ts).
const customerUrl = (host: string, path = '/') => `http://${host}${path}`

test('connect a domain, serve it through the proxy, remove it', async ({
  page,
  context,
}) => {
  const host = `go-${Date.now()}.acme.test`
  const brand = page.getByTestId('brand-acme')

  await page.goto('/')
  await brand.getByLabel('Domain').fill(host)
  await brand.getByLabel('DNS contact email (optional)').fill('it@acme.test')
  await brand.getByRole('button', { name: 'Add domain' }).click()
  const row = page.getByTestId(`domain-${host}`)
  await expect(row).toContainText('Waiting for DNS')

  // The mock marks *.test as pointed after ~2s and sends webhooks.
  await expect(async () => {
    await page.reload()
    await expect(row).toContainText('Live')
  }).toPass({ timeout: 15_000 })

  // The short link redirects. Only the 302 matters here, so the
  // navigation to example.com is allowed to fail.
  const tab = await context.newPage()
  const [redirect] = await Promise.all([
    tab.waitForResponse(customerUrl(host, '/launch')),
    tab.goto(customerUrl(host, '/launch')).catch(() => {}),
  ])
  await tab.close()
  expect(redirect.status()).toBe(302)
  expect(redirect.headers().location).toBe(
    'https://example.com/acme/launch-day',
  )

  await page.goto(customerUrl(host))
  await expect(page.getByRole('heading', { name: 'Acme Rockets' })).toBeVisible()
  await expect(page.getByText(`${host}/launch`)).toBeVisible()

  // A form on the customer's domain, through the proxy. The app sees
  // Host: app:3000 and Origin: http://<host>.
  await page.getByLabel('Which link is broken?').fill(`${host}/old`)
  await page.getByRole('button', { name: 'Report' }).click()
  await expect(page.getByRole('status')).toHaveText(
    'Thanks. Acme Rockets will take a look.',
  )
  await expect(page).toHaveURL(customerUrl(host, '/?reported'))

  await page.goto('/')
  await row.getByRole('button', { name: 'Remove' }).click()
  await expect(row).toBeHidden()
  const gone = await page.goto(customerUrl(host))
  expect(gone?.status()).toBe(404)
})

test('a domain that does not point at us stays pending', async ({ page }) => {
  const host = `go-${Date.now()}.globex.example`
  const brand = page.getByTestId('brand-globex')

  await page.goto('/')
  await brand.getByLabel('Domain').fill(host)
  await brand.getByRole('button', { name: 'Add domain' }).click()
  const row = page.getByTestId(`domain-${host}`)
  await row.getByRole('button', { name: 'Check DNS' }).click()
  await expect(row).toContainText('Waiting for DNS')

  await row.getByRole('button', { name: 'Remove' }).click()
  await expect(row).toBeHidden()
})

test('rejects our own domains and invalid input', async ({ page }) => {
  const brand = page.getByTestId('brand-acme')
  await page.goto('/')
  await brand.getByLabel('Domain').fill('admin.snip.example')
  await brand.getByRole('button', { name: 'Add domain' }).click()
  await expect(brand.getByRole('alert')).toHaveText("That's one of our domains.")

  await brand.getByLabel('Domain').fill('not a domain')
  await brand.getByRole('button', { name: 'Add domain' }).click()
  await expect(brand.getByRole('alert')).toHaveText("That isn't a valid domain.")
})

test('requests that skip the proxy cannot claim a domain', async ({
  request,
}) => {
  const spoofed = await request.get('/launch', {
    headers: { 'X-Served-For': 'go.acme.test' },
  })
  expect(spoofed.status()).toBe(403)

  const wrongToken = await request.get('/launch', {
    headers: {
      'X-Served-For': 'go.acme.test',
      'X-SaaS-Domains-Auth-Token': 'guess',
    },
  })
  expect(wrongToken.status()).toBe(403)
})

test('webhooks need the right token', async ({ request }) => {
  const res = await request.post('/webhooks/saascustomdomains?token=x', {
    data: { event: 'domain_dns_proxy_record_valid', domain: { uuid: 'x' } },
  })
  expect(res.status()).toBe(401)
})

test('unknown domains get a 404', async ({ page }) => {
  const res = await page.goto(customerUrl('unknown.test'))
  expect(res?.status()).toBe(404)
})
