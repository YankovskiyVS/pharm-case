import { expect, type Page } from "@playwright/test";
export class LoginPage {
  constructor(
    private page: Page,
    private portal: "doctor" | "patient",
  ) {}
  async open() {
    await this.page.goto(`/${this.portal}/`);
  }
  async login(email: string, password: string, code = "") {
    await this.page.getByLabel("Электронная почта").fill(email);
    await this.page.getByLabel("Пароль", { exact: true }).fill(password);
    if (this.portal === "doctor")
      await this.page.getByLabel("Код аутентификатора").fill(code);
    await this.page.getByRole("button", { name: "Войти в кабинет" }).click();
    await expect(
      this.page.getByRole("button", { name: "Выйти" }),
    ).toBeVisible();
  }
}
