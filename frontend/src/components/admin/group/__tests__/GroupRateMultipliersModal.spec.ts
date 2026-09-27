import { flushPromises, mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { getGroupRateMultipliers, batchSetGroupRateMultipliers } = vi.hoisted(() => ({
  getGroupRateMultipliers: vi.fn(),
  batchSetGroupRateMultipliers: vi.fn(),
}));

vi.mock("@/api/admin", () => ({
  adminAPI: {
    groups: { getGroupRateMultipliers, batchSetGroupRateMultipliers },
    users: { list: vi.fn() },
  },
}));

vi.mock("@/stores/app", () => ({
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() }),
}));

vi.mock("vue-i18n", async () => {
  const actual = await vi.importActual<typeof import("vue-i18n")>("vue-i18n");
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  };
});

import GroupRateMultipliersModal from "../GroupRateMultipliersModal.vue";

const entry = (user_id: number, rate_multiplier: number) => ({
  user_id,
  user_name: `u${user_id}`,
  user_email: `u${user_id}@x.com`,
  user_notes: "",
  user_status: "active",
  rate_multiplier,
  rpm_override: null,
});

const mountModal = async (previousGroupRate: number | null = null) => {
  const wrapper = mount(GroupRateMultipliersModal, {
    props: {
      show: false,
      group: { id: 7, name: "g", platform: "openai", rate_multiplier: 1.2 } as any,
      previousGroupRate,
    },
    global: {
      stubs: {
        BaseDialog: { template: "<div><slot /></div>" },
        Pagination: true,
        Icon: true,
        PlatformIcon: true,
      },
    },
  });
  await wrapper.setProps({ show: true });
  await flushPromises();
  return wrapper;
};

const rowEmails = (wrapper: any) =>
  wrapper.findAll("tbody tr").map((tr: any) => tr.findAll("td")[1].text());

describe("GroupRateMultipliersModal", () => {
  beforeEach(() => {
    getGroupRateMultipliers.mockReset();
    batchSetGroupRateMultipliers.mockReset();
    getGroupRateMultipliers.mockResolvedValue([entry(1, 1.5), entry(2, 0.6), entry(3, 1.2)]);
    batchSetGroupRateMultipliers.mockResolvedValue({ message: "ok" });
  });

  it("sorts by rate ascending and shows the diff against the group rate", async () => {
    const wrapper = await mountModal();
    expect(rowEmails(wrapper)).toEqual(["u2@x.com", "u3@x.com", "u1@x.com"]);
    const diffs = wrapper.findAll("tbody tr").map((tr) => tr.findAll("td")[7].text());
    expect(diffs).toEqual(["-50.0%", "admin.groups.sameAsGroupRate", "+25.0%"]);
  });

  it("filters to users below the group rate", async () => {
    const wrapper = await mountModal();
    await wrapper.find("label input[type=checkbox]").setValue(true);
    expect(rowEmails(wrapper)).toEqual(["u2@x.com"]);
  });

  it("raises rates below the floor and saves", async () => {
    const wrapper = await mountModal();
    const floorInput = wrapper.findAll("input[type=number]")[2];
    await floorInput.setValue(1);
    const applyButtons = wrapper.findAll("button").filter((b) => b.text() === "admin.groups.applyMultiplier");
    await applyButtons[1].trigger("click");
    await wrapper.findAll("button").find((b) => b.text() === "common.save")!.trigger("click");
    await flushPromises();
    expect(batchSetGroupRateMultipliers).toHaveBeenCalledWith(7, [
      { user_id: 1, rate_multiplier: 1.5 },
      { user_id: 2, rate_multiplier: 1 },
      { user_id: 3, rate_multiplier: 1.2 },
    ]);
  });

  it("prefills the proportional factor after a group rate change and only scales selected rows", async () => {
    const wrapper = await mountModal(1);
    const factorInput = wrapper.findAll("input[type=number]")[1];
    expect((factorInput.element as HTMLInputElement).value).toBe("1.2");
    // 勾选第一行（u2，0.6）
    await wrapper.findAll("tbody tr")[0].find("input[type=checkbox]").setValue(true);
    const applyButtons = wrapper.findAll("button").filter((b) => b.text() === "admin.groups.applyMultiplier");
    await applyButtons[0].trigger("click");
    await wrapper.findAll("button").find((b) => b.text() === "common.save")!.trigger("click");
    await flushPromises();
    expect(batchSetGroupRateMultipliers).toHaveBeenCalledWith(7, [
      { user_id: 1, rate_multiplier: 1.5 },
      { user_id: 2, rate_multiplier: 0.72 },
      { user_id: 3, rate_multiplier: 1.2 },
    ]);
  });

  it("removes overrides so users follow the group rate", async () => {
    const wrapper = await mountModal();
    await wrapper.findAll("tbody tr")[0].find("input[type=checkbox]").setValue(true);
    await wrapper.findAll("button").find((b) => b.text() === "admin.groups.followGroupRate")!.trigger("click");
    await wrapper.findAll("button").find((b) => b.text() === "common.save")!.trigger("click");
    await flushPromises();
    expect(batchSetGroupRateMultipliers).toHaveBeenCalledWith(7, [
      { user_id: 1, rate_multiplier: 1.5 },
      { user_id: 3, rate_multiplier: 1.2 },
    ]);
  });
});
