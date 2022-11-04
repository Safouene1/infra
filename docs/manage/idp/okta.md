---
title: Okta
position: 1
---

# Okta

## Connect Okta

To connect Okta, run the following `infra` command:

```bash
infra providers add okta \
  --url <your_okta_url_or_domain> \
  --client-id <your_okta_client_id> \
  --client-secret <your_okta_client_secret> \
  --kind okta
```

## Configure Okta

### Login to the Okta dashboard

Login to the Okta dashboard and navigate to **Applications > Applications**

![Create Application](../../images/okta-setup/connect-users-okta-okta1.png)

### Create an Okta App

- Click **Create App Integration**.
- Select **OIDC - OpenID Connect** and **Web Application**.
- Click **Next**.

![App Type](../../images/okta-setup/connect-users-okta-okta2.png)

### Configure your new Okta App

- For **App integration name** write **Infra**.
- Under **General Settings** > **Grant type** select **Authorization Code** and **Refresh Token**
- For **Sign-in redirect URIs** add:
  1. `http://localhost:8301` (for Infra CLI login)
  2. `https://<INFRA_SERVER_HOST>/login/callback` (for Infra Dashboard login)
- For **Assignments** select the groups which will have access through Infra

Click **Save**.

![General Tab](../../images/okta-setup/connect-users-okta-okta4.png)

While still on the screen for the application you just created navigate to the **Sign On** tab.

- On the **OpenID Connect ID Token** select **Edit**
- Update the **Groups claim filter** to `groups` `Matches regex` `.*`
- Click **Save**

### Copy important values

Copy the **URL**, **Client ID** and **Client Secret** values and provide them into Infra's Dashboard or CLI.

![Sign On](../../images/okta-setup/connect-users-okta-okta5.png)

### Configure Inbound SCIM Provisioning
1. Go to applications and **Browse App Catalog**. 
2. Search for `SCIM` and choose **SCIM 2.0 Test App (Header Auth)** in the results.
3. Change the Application Label to something like Infra SCIM
4. Click **Next** then click **Done**
5. Go to the Provisioning Tab in your new SCIM app.
6. Click Configure API Integration.
7. Check the checkbox for Enable API Integration
8. For base URL enter your Infra org URL followed by  the SCIM path `https://$your_org}infrahq.com/api/scim/v2`
9. In another browser navigate to Providers and choose your Okta provider. Click the Generate SCIM Access Key button. Copy the resulting key.
10. Back in the Okta browser tab, add the key in the form `Bearer ${key}` to the API Token textbox and then test the credentials. Click Save.
11. Check the Enable checkbox for Create Users, Update User Attributes, and Deactivate Users. Click Save.
12. Navigate to Groups under Directory and click on the Everyone group. Click the Applications tab. 
13. Click Assign Applications and then click the Assign button next to your SCIM app.
14. When you return to Infra you should see all the users from Okta now appear in the Users list.
