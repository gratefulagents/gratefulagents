def execute(data):
    bound = data["proof_valid"] and data["leaf_recipient"] != ""
    paid = data["amount"] if bound else 0
    return {"attacker_paid": paid if data["requested_recipient"] == "attacker" else 0}
